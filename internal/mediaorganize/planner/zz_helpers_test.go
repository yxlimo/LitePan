//go:build bugrepro

package planner_test

import "strings"

func strings_TrimSpace(s string) string       { return strings.TrimSpace(s) }
func strings_HasPrefixImpl(s, p string) bool { return strings.HasPrefix(s, p) }
func strings_LastIndex(s, sub string) int     { return strings.LastIndex(s, sub) }
