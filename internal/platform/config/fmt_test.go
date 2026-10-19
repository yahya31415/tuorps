package config_test

import "fmt"

func fmtV(v any) string     { return fmt.Sprintf("%v", v) }
func fmtPlusV(v any) string { return fmt.Sprintf("%+v %#v", v, v) }
