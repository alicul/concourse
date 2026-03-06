// Package windowscontainerd provides a Garden backend implementation backed by
// containerd for Windows containers.
//
// This is a separate package from the Linux containerd backend (worker/runtime)
// because Windows containers use fundamentally different OCI spec structures,
// networking (HNS vs CNI), and process models. The Linux backend relies heavily
// on Linux-specific concepts (cgroups, seccomp, user namespaces, iptables)
// that have no equivalent on Windows.
//
// This package reuses the cross-platform libcontainerd client to communicate
// with the containerd daemon, while providing Windows-appropriate container
// configuration and lifecycle management.
package windowscontainerd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"code.cloudfoundry.org/garden"
	"github.com/concourse/concourse/worker/runtime/libcontainerd"
	containerd "github.com/containerd/containerd/v2/client"
	"github.com/containerd/containerd/v2/core/containers"
	"github.com/containerd/containerd/v2/core/images"
	"github.com/containerd/containerd/v2/pkg/cio"
	"github.com/containerd/containerd/v2/pkg/oci"
	"github.com/containerd/errdefs"
	specs "github.com/opencontainers/runtime-spec/specs-go"
)

// ImageClient provides containerd image import and container-from-image
// creation. Implemented by *libcontainerd.client but kept as an interface
// for testability.
type ImageClient interface {
	ImportImage(ctx context.Context, reader io.Reader, refName string) (images.Image, error)
	GetImage(ctx context.Context, ref string) (containerd.Image, error)
	UnpackImage(ctx context.Context, image containerd.Image, snapshotter string) error
	NewContainerFromImage(ctx context.Context, id string, image containerd.Image, labels map[string]string, snapshotter string, specOpts ...oci.SpecOpts) (containerd.Container, error)
}

type GardenBackendOpt func(b *GardenBackend)

// GardenBackend implements garden.Backend using containerd on Windows.
// Unlike the Linux backend, this does not use seccomp, user namespaces,
// Linux capabilities, or cgroup-based resource control.
type GardenBackend struct {
	client            libcontainerd.Client
	imageClient       ImageClient
	maxContainers     int
	requestTimeout    time.Duration
	dnsServers        []string
	workDir           string
	hypervIsolation   bool
}

var _ garden.Backend = (*GardenBackend)(nil)

func WithRequestTimeout(t time.Duration) GardenBackendOpt {
	return func(b *GardenBackend) {
		b.requestTimeout = t
	}
}

func WithMaxContainers(limit int) GardenBackendOpt {
	return func(b *GardenBackend) {
		b.maxContainers = limit
	}
}

func WithDNSServers(servers []string) GardenBackendOpt {
	return func(b *GardenBackend) {
		b.dnsServers = servers
	}
}

func WithWorkDir(dir string) GardenBackendOpt {
	return func(b *GardenBackend) {
		b.workDir = dir
	}
}

func WithImageClient(ic ImageClient) GardenBackendOpt {
	return func(b *GardenBackend) {
		b.imageClient = ic
	}
}

func WithHyperVIsolation(enabled bool) GardenBackendOpt {
	return func(b *GardenBackend) {
		b.hypervIsolation = enabled
	}
}

func NewGardenBackend(client libcontainerd.Client, opts ...GardenBackendOpt) (GardenBackend, error) {
	if client == nil {
		return GardenBackend{}, fmt.Errorf("nil client")
	}

	b := GardenBackend{
		client: client,
	}
	for _, opt := range opts {
		opt(&b)
	}

	return b, nil
}

func (b *GardenBackend) Start() error {
	return b.client.Init()
}

func (b *GardenBackend) Stop() error {
	return b.client.Stop()
}

func (b *GardenBackend) Ping() error {
	return b.client.Version(context.Background())
}

func (b *GardenBackend) Create(gdnSpec garden.ContainerSpec) (garden.Container, error) {
	ctx := context.Background()

	if err := b.checkContainerCapacity(ctx); err != nil {
		return nil, err
	}

	rootfsPath, err := resolveRootFSPath(gdnSpec)
	if err != nil {
		return nil, fmt.Errorf("resolve rootfs: %w", err)
	}

	volumeDir := filepath.Dir(rootfsPath)
	imageTar := filepath.Join(volumeDir, "image.tar")

	if b.imageClient != nil {
		if _, statErr := os.Stat(imageTar); statErr == nil {
			return b.createFromOCITarball(ctx, gdnSpec, imageTar)
		}
	}

	return b.createFromRootFS(ctx, gdnSpec, rootfsPath)
}

func resolveRootFSPath(gdnSpec garden.ContainerSpec) (string, error) {
	raw := gdnSpec.RootFSPath
	if raw == "" {
		raw = gdnSpec.Image.URI
	}
	return rootfsDir(raw)
}

func (b *GardenBackend) createFromRootFS(ctx context.Context, gdnSpec garden.ContainerSpec, rootfsPath string) (garden.Container, error) {
	scratchDir := filepath.Join(b.workDir, "scratch", gdnSpec.Handle)
	if err := os.MkdirAll(scratchDir, 0755); err != nil {
		return nil, fmt.Errorf("create scratch dir: %w", err)
	}

	ociSpec, err := OciSpec(gdnSpec, scratchDir)
	if err != nil {
		return nil, fmt.Errorf("windows oci spec: %w", err)
	}

	labels, err := propertiesToLabels(gdnSpec.Properties)
	if err != nil {
		return nil, fmt.Errorf("convert properties to labels: %w", err)
	}

	cont, err := b.client.NewContainer(ctx, gdnSpec.Handle, labels, ociSpec)
	if err != nil {
		return nil, fmt.Errorf("new container: %w", err)
	}

	task, err := cont.NewTask(ctx, cio.NullIO)
	if err != nil {
		return nil, fmt.Errorf("new task: %w", err)
	}

	if err := task.Start(ctx); err != nil {
		return nil, fmt.Errorf("start task: %w", err)
	}

	return NewContainer(cont), nil
}

func (b *GardenBackend) createFromOCITarball(ctx context.Context, gdnSpec garden.ContainerSpec, imageTarPath string) (garden.Container, error) {
	volumeDir := filepath.Dir(imageTarPath)
	refName := readImageRef(volumeDir)

	f, err := os.Open(imageTarPath)
	if err != nil {
		return nil, fmt.Errorf("open image tarball: %w", err)
	}
	defer f.Close()

	imgMeta, err := b.imageClient.ImportImage(ctx, f, refName)
	if err != nil {
		return nil, fmt.Errorf("import image: %w", err)
	}

	image, err := b.imageClient.GetImage(ctx, imgMeta.Name)
	if err != nil {
		return nil, fmt.Errorf("get imported image: %w", err)
	}

	const snapshotter = "windows"
	if err := b.imageClient.UnpackImage(ctx, image, snapshotter); err != nil {
		return nil, fmt.Errorf("unpack image: %w", err)
	}

	mounts, err := ociSpecBindMounts(gdnSpec.BindMounts)
	if err != nil {
		return nil, fmt.Errorf("bind mounts: %w", err)
	}

	labels, err := propertiesToLabels(gdnSpec.Properties)
	if err != nil {
		return nil, fmt.Errorf("convert properties to labels: %w", err)
	}

	specOpts := []oci.SpecOpts{
		oci.WithImageConfig(image),
		oci.WithProcessArgs("cmd.exe", "/S", "/C", "ping -t localhost > NUL"),
		oci.WithProcessCwd(`C:\`),
		oci.WithUsername("ContainerAdministrator"),
		oci.WithHostname(gdnSpec.Handle),
		oci.WithEnv(gdnSpec.Env),
		oci.WithMounts(mounts),
		withWindowsNetworkConfig(),
	}

	if b.hypervIsolation {
		specOpts = append(specOpts, withHyperVIsolation())
	}

	cont, err := b.imageClient.NewContainerFromImage(ctx, gdnSpec.Handle, image, labels, snapshotter, specOpts...)
	if err != nil {
		return nil, fmt.Errorf("new container from image: %w", err)
	}

	task, err := cont.NewTask(ctx, cio.NullIO)
	if err != nil {
		return nil, fmt.Errorf("new task: %w", err)
	}

	if err := task.Start(ctx); err != nil {
		return nil, fmt.Errorf("start task: %w", err)
	}

	return NewContainer(cont), nil
}

// readImageRef reads repository and tag files written by the registry-image
// resource to construct a containerd image reference name.
func readImageRef(volumeDir string) string {
	repo := readFileString(filepath.Join(volumeDir, "repository"))
	tag := readFileString(filepath.Join(volumeDir, "tag"))
	if repo == "" {
		return "imported"
	}
	if tag == "" {
		return repo
	}
	return repo + ":" + tag
}

func readFileString(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// withHyperVIsolation enables Hyper-V isolation for the container. This runs
// the container inside a lightweight VM, which allows running containers whose
// OS build doesn't match the host build (e.g. nanoserver:ltsc2025 on a newer
// Windows 11 build). Requires Hyper-V to be enabled on the host.
func withHyperVIsolation() oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		if s.Windows == nil {
			s.Windows = &specs.Windows{}
		}
		s.Windows.HyperV = &specs.WindowsHyperV{}
		return nil
	}
}

// withWindowsNetworkConfig sets AllowUnqualifiedDNSQuery for Windows containers.
func withWindowsNetworkConfig() oci.SpecOpts {
	return func(_ context.Context, _ oci.Client, _ *containers.Container, s *specs.Spec) error {
		if s.Windows == nil {
			s.Windows = &specs.Windows{}
		}
		s.Windows.Network = &specs.WindowsNetwork{
			AllowUnqualifiedDNSQuery: true,
		}
		return nil
	}
}

func (b *GardenBackend) Destroy(handle string) error {
	if handle == "" {
		return fmt.Errorf("empty handle")
	}

	ctx := context.Background()

	container, err := b.client.GetContainer(ctx, handle)
	if err != nil {
		return fmt.Errorf("get container: %w", err)
	}

	task, err := container.Task(ctx, cio.Load)
	if err != nil {
		if !errdefs.IsNotFound(err) {
			return fmt.Errorf("task lookup: %w", err)
		}

		return container.Delete(ctx)
	}

	if err := task.Kill(ctx, WindowsTerminateSignal); err != nil {
		if !errdefs.IsNotFound(err) {
			return fmt.Errorf("killing task: %w", err)
		}
	}

	exitCh, err := task.Wait(ctx)
	if err == nil {
		select {
		case <-exitCh:
		case <-time.After(10 * time.Second):
		}
	}

	_, err = task.Delete(ctx, containerd.WithProcessKill)
	if err != nil {
		return fmt.Errorf("task delete: %w", err)
	}

	return container.Delete(ctx)
}

func (b *GardenBackend) Containers(properties garden.Properties) ([]garden.Container, error) {
	filters, err := propertiesToFilterList(properties)
	if err != nil {
		return nil, err
	}

	res, err := b.client.Containers(context.Background(), filters...)
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}

	containers := make([]garden.Container, len(res))
	for i, c := range res {
		containers[i] = NewContainer(c)
	}

	return containers, nil
}

func (b *GardenBackend) Lookup(handle string) (garden.Container, error) {
	if handle == "" {
		return nil, fmt.Errorf("empty handle")
	}

	c, err := b.client.GetContainer(context.Background(), handle)
	if err != nil {
		return nil, fmt.Errorf("get container: %w", err)
	}

	return NewContainer(c), nil
}

func (b *GardenBackend) GraceTime(container garden.Container) time.Duration {
	property, err := container.Property(GraceTimeKey)
	if err != nil {
		return 0
	}

	var duration time.Duration
	_, err = fmt.Sscanf(property, "%d", &duration)
	if err != nil {
		return 0
	}

	return duration
}

func (b *GardenBackend) Capacity() (garden.Capacity, error) {
	return garden.Capacity{}, fmt.Errorf("not implemented")
}

func (b *GardenBackend) BulkInfo(handles []string) (map[string]garden.ContainerInfoEntry, error) {
	return nil, fmt.Errorf("not implemented")
}

func (b *GardenBackend) BulkMetrics(handles []string) (map[string]garden.ContainerMetricsEntry, error) {
	return nil, fmt.Errorf("not implemented")
}

func (b *GardenBackend) checkContainerCapacity(ctx context.Context) error {
	if b.maxContainers == 0 {
		return nil
	}

	containers, err := b.client.Containers(ctx)
	if err != nil {
		return fmt.Errorf("getting list of containers: %w", err)
	}

	if len(containers) >= b.maxContainers {
		return fmt.Errorf("max containers reached")
	}
	return nil
}

// propertiesToLabels mirrors the Linux runtime's property-to-label conversion.
// Containerd restricts label key+value to 4096 bytes, so values are chunked.
func propertiesToLabels(properties garden.Properties) (map[string]string, error) {
	const maxLabelLen = 4096
	const maxKeyLen = maxLabelLen / 2

	labelSet := map[string]string{}
	for key, value := range properties {
		sequenceNum := 0
		if len(key) > maxKeyLen {
			return nil, fmt.Errorf("property name %q is too long", key[:32]+"...")
		}
		value = strings.ToValidUTF8(value, string(utf8.RuneError))
		for {
			chunkKey := key + "." + strconv.Itoa(sequenceNum)
			valueLen := min(maxLabelLen-len(chunkKey), len(value))

			labelSet[chunkKey] = value[:valueLen]
			value = value[valueLen:]

			if len(value) == 0 {
				break
			}
			sequenceNum++
		}
	}
	return labelSet, nil
}

func labelsToProperties(labels map[string]string) garden.Properties {
	properties := garden.Properties{}
	for len(labels) > 0 {
		var key string
		for k := range labels {
			key = k
			break
		}

		chunkSequenceStart := strings.LastIndexByte(key, '.')
		if chunkSequenceStart < 0 {
			delete(labels, key)
			continue
		}

		propertyName := key[:chunkSequenceStart]

		var property strings.Builder
		for sequenceNum := 0; ; sequenceNum++ {
			chunkKey := propertyName + "." + strconv.Itoa(sequenceNum)
			chunkValue, ok := labels[chunkKey]
			if !ok {
				break
			}
			delete(labels, chunkKey)
			property.WriteString(chunkValue)
		}

		if property.Len() == 0 {
			delete(labels, key)
			continue
		}

		properties[propertyName] = property.String()
	}

	return properties
}

func propertiesToFilterList(properties garden.Properties) ([]string, error) {
	for k, v := range properties {
		if k == "" || v == "" {
			return nil, fmt.Errorf("key or value must not be empty")
		}
	}

	labels, err := propertiesToLabels(properties)
	if err != nil {
		return nil, err
	}

	filters := make([]string, 0, len(labels))
	for k, v := range labels {
		filters = append(filters, "labels."+k+"=="+v)
	}

	return filters, nil
}
