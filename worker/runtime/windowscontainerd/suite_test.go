package windowscontainerd_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

func TestSuite(t *testing.T) {
	suite.Run(t, &BackendSuite{Assertions: require.New(t)})
	suite.Run(t, &ContainerSuite{Assertions: require.New(t)})
	suite.Run(t, &ProcessSuite{Assertions: require.New(t)})
	suite.Run(t, &SpecSuite{Assertions: require.New(t)})
}
