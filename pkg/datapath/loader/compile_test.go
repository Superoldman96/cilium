// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package loader

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/cilium/hive/hivetest"
	"github.com/stretchr/testify/require"

	"github.com/cilium/cilium/pkg/testutils"
)

func TestPrivilegedCompile(t *testing.T) {
	testutils.PrivilegedTest(t)

	debugOutput := func(p *progInfo) *progInfo {
		cpy := *p
		cpy.Output = cpy.Source
		cpy.OutputType = outputSource
		return &cpy
	}

	dirs := getDirs(t)
	for _, prog := range []*progInfo{
		epProg,
		hostEpProg,
		debugOutput(epProg),
		debugOutput(hostEpProg),
	} {
		name := fmt.Sprintf("%s:%s", prog.OutputType, prog.Output)
		t.Run(name, func(t *testing.T) {
			logger := hivetest.Logger(t)
			path, err := compile(context.Background(), logger, prog, dirs)
			require.NoError(t, err)

			stat, err := os.Stat(path)
			require.NoError(t, err)
			require.False(t, stat.IsDir())
			require.NotZero(t, stat.Size())
		})
	}
}
