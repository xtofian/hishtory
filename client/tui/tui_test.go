package tui

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCalculateWordBoundaries(t *testing.T) {
	require.Equal(t, []int{0, 3}, calculateWordBoundaries("foo"))
	require.Equal(t, []int{0, 3, 7}, calculateWordBoundaries("foo bar"))
	require.Equal(t, []int{0, 3, 7}, calculateWordBoundaries("foo-bar"))
	require.Equal(t, []int{0, 3, 7, 11}, calculateWordBoundaries("foo-bar baz"))
	require.Equal(t, []int{0, 3, 10, 16}, calculateWordBoundaries("foo-- -bar - baz"))
	require.Equal(t, []int{0, 3}, calculateWordBoundaries("foo    "))
}

func TestSanitizeEscapeCodes(t *testing.T) {
	require.Equal(t, "foo", sanitizeEscapeCodes("foo"))
	require.Equal(t, "foo\x1b[31mbar", sanitizeEscapeCodes("foo\x1b[31mbar"))
	require.Equal(t, "", sanitizeEscapeCodes("11;rgb:1c1c/1c1c/1c1c"))
	require.Equal(t, "foo  bar", sanitizeEscapeCodes("foo 11;rgb:1c1c/1c1c/1c1c bar"))
}

func TestWrapWithLabel(t *testing.T) {
	require.Equal(t, []string{"Command: ls"}, wrapWithLabel("Command: ", "ls", 80, 4))
	require.Equal(t, []string{"Command: "}, wrapWithLabel("Command: ", "", 80, 4))

	// Continuation lines are indented by the width of the label
	require.Equal(t, []string{
		"Command: aaaaa",
		"         bbbbb",
		"         cc",
	}, wrapWithLabel("Command: ", "aaaaabbbbbcc", 14, 4))

	// A value that is too long to fit is truncated with an ellipsis
	require.Equal(t, []string{
		"Command: aaaaa",
		"         bbbb…",
	}, wrapWithLabel("Command: ", "aaaaabbbbbcc", 14, 2))

	// Newlines are flattened so that a multi-line command can't blow up the fixed-height preview
	require.Equal(t, []string{"Command: foo bar baz"}, wrapWithLabel("Command: ", "foo\nbar\r\nbaz", 80, 4))

	// A terminal too narrow to fit even the label still makes progress rather than looping forever
	require.Equal(t, []string{"Command: a", "         …"}, wrapWithLabel("Command: ", "abc", 5, 2))
}
