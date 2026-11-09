package tui

import "fmt"

func sprintType(v any) string { return fmt.Sprintf("%T", v) }
