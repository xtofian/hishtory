package cmd

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExtractExportFormat(t *testing.T) {
	testcases := []struct {
		args           []string
		expectedFormat exportFormat
		expectedArgs   []string
		expectedErr    bool
	}{
		{args: []string{}, expectedFormat: exportFormatText, expectedArgs: []string{}},
		{args: []string{"foo", "cwd:/tmp/"}, expectedFormat: exportFormatText, expectedArgs: []string{"foo", "cwd:/tmp/"}},
		{args: []string{"--format=jsonl"}, expectedFormat: exportFormatJsonl, expectedArgs: []string{}},
		{args: []string{"--format", "jsonl"}, expectedFormat: exportFormatJsonl, expectedArgs: []string{}},
		{args: []string{"foo", "--format=jsonl", "bar"}, expectedFormat: exportFormatJsonl, expectedArgs: []string{"foo", "bar"}},
		{args: []string{"--format=text", "foo"}, expectedFormat: exportFormatText, expectedArgs: []string{"foo"}},
		{args: []string{"--format=csv"}, expectedErr: true},
		{args: []string{"--format"}, expectedErr: true},
	}
	for _, tc := range testcases {
		format, args, err := extractExportFormat(tc.args)
		if tc.expectedErr {
			require.Error(t, err, "args=%#v", tc.args)
			continue
		}
		require.NoError(t, err, "args=%#v", tc.args)
		require.Equal(t, tc.expectedFormat, format, "args=%#v", tc.args)
		require.Equal(t, tc.expectedArgs, args, "args=%#v", tc.args)
	}
}
