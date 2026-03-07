package windowscontainerd

import (
	"strings"
	"testing"

	"code.cloudfoundry.org/garden"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestToWindowsPath(t *testing.T) {
	tests := []struct {
		desc     string
		input    string
		expected string
	}{
		{
			desc:     "Unix absolute path gets drive letter",
			input:    "/scratch/volume",
			expected: `C:\scratch\volume`,
		},
		{
			desc:     "Windows absolute path remains",
			input:    `C:\data\folder`,
			expected: `C:\data\folder`,
		},
		{
			desc:     "Relative path just swapped",
			input:    "local/path",
			expected: `local\path`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			actual := toWindowsPath(tc.input)
			assert.Equal(t, tc.expected, actual)
		})
	}
}

func TestEncodeDecodeProperties(t *testing.T) {
	// Generate massive string
	largeValue := strings.Repeat("a", 5000)

	props := garden.Properties{
		"small": "value",
		"large": largeValue,
	}

	encoded, err := encodeProperties(props)
	require.NoError(t, err)

	assert.Equal(t, "value", encoded["small"])

	// Check chunking limits for "large"
	assert.Len(t, encoded["large.0"], 4096)
	assert.Len(t, encoded["large.1"], 904) // 5000 - 4096

	decoded := decodeProperties(encoded)
	assert.Equal(t, props["small"], decoded["small"])
	assert.Equal(t, props["large"], decoded["large"])
}

func TestResolveRootFSPath(t *testing.T) {
	spec := garden.ContainerSpec{
		RootFSPath: "raw://C%3A%5Cconcourse%5Cwork%5Cvolumes%5Crootfs",
	}

	path, err := resolveRootFSPath(spec)
	require.NoError(t, err)
	assert.Equal(t, `C:\concourse\work\volumes\rootfs`, path)

	spec2 := garden.ContainerSpec{
		Image: garden.ImageRef{URI: "raw://D%3A%5Cfallback%5Crootfs"},
	}

	path2, err := resolveRootFSPath(spec2)
	require.NoError(t, err)
	assert.Equal(t, `D:\fallback\rootfs`, path2)
}
