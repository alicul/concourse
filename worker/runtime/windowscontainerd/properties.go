package windowscontainerd

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"code.cloudfoundry.org/garden"
)

func encodeProperties(properties garden.Properties) (map[string]string, error) {
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
			valueLen := maxLabelLen - len(chunkKey)
			if valueLen > len(value) {
				valueLen = len(value)
			}

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

func decodeProperties(labels map[string]string) garden.Properties {
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
