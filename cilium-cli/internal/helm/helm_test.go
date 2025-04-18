// SPDX-License-Identifier: Apache-2.0
// Copyright Authors of Cilium

package helm

import (
	"reflect"
	"testing"

	"github.com/blang/semver/v4"
	"github.com/stretchr/testify/assert"

	"github.com/cilium/cilium/cilium-cli/defaults"
)

func TestResolveHelmChartVersion(t *testing.T) {
	type args struct {
		versionFlag        string
		chartDirectoryFlag string
	}
	tests := []struct {
		name    string
		args    args
		want    semver.Version
		wantErr bool
	}{
		{
			name:    "valid-version",
			args:    args{versionFlag: "v1.11.5", chartDirectoryFlag: ""},
			want:    semver.Version{Major: 1, Minor: 11, Patch: 5},
			wantErr: false,
		},
		{
			name:    "missing-version",
			args:    args{versionFlag: "v0.0.0", chartDirectoryFlag: ""},
			wantErr: true,
		},
		{
			name:    "invalid-version",
			args:    args{versionFlag: "random-version", chartDirectoryFlag: ""},
			wantErr: true,
		},
		{
			name:    "valid-chart-directory",
			args:    args{versionFlag: "", chartDirectoryFlag: "./testdata"},
			want:    semver.Version{Major: 1, Minor: 2, Patch: 3},
			wantErr: false,
		},
		{
			name:    "invalid-chart-directory",
			args:    args{versionFlag: "", chartDirectoryFlag: "/invalid/chart-directory"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _, err := ResolveHelmChartVersion(tt.args.versionFlag, tt.args.chartDirectoryFlag, defaults.HelmRepository)
			if (err != nil) != tt.wantErr {
				t.Errorf("ResolveHelmChartVersion() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ResolveHelmChartVersion() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestParseVals(t *testing.T) {
	tests := []struct {
		name    string
		input   []string
		want    map[string]any
		wantErr bool
	}{
		{
			name:    "simple-val",
			input:   []string{"simple=true"},
			want:    map[string]any{"simple": true},
			wantErr: false,
		},
		{
			name:    "two-levels",
			input:   []string{"two.levels=true"},
			want:    map[string]any{"two": map[string]any{"levels": true}},
			wantErr: false,
		},
		{
			name:    "multiple-keys",
			input:   []string{"multiple=true", "keys=true"},
			want:    map[string]any{"multiple": true, "keys": true},
			wantErr: false,
		},
		{
			name:    "string-type",
			input:   []string{"string=testval"},
			want:    map[string]any{"string": "testval"},
			wantErr: false,
		},
		{
			name:    "mixed-type",
			input:   []string{"string=testval", "bool=false"},
			want:    map[string]any{"string": "testval", "bool": false},
			wantErr: false,
		},
		{
			name:  "mixed-levels",
			input: []string{"two.levels=true", "three.levels.deep=true"},
			want: map[string]any{
				"two":   map[string]any{"levels": true},
				"three": map[string]any{"levels": map[string]any{"deep": true}},
			},
			wantErr: false,
		},
		{
			name:    "invalid-input",
			input:   []string{"invalid"},
			wantErr: true,
		},
		{
			name:    "mixed-invalid",
			input:   []string{"testkey=val", "invalid"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseVals(tt.input)
			if (err != nil) != tt.wantErr {
				t.Errorf("ParseVals() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ParseVals() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestGetDefaultVersionString(t *testing.T) {
	versionString := GetDefaultVersionString()
	version, err := semver.ParseTolerant(versionString)
	assert.NoError(t, err)
	assert.Nil(t, version.Pre)
}
