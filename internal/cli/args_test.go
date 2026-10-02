package cli

import (
	"flag"
	"io"
	"reflect"
	"testing"
)

func TestParseInterspersed(t *testing.T) {
	cases := []struct {
		args, want []string
		refresh    bool
		lane       string
	}{
		{[]string{"q"}, []string{"q"}, false, "answer"},
		{[]string{"q", "--refresh"}, []string{"q"}, true, "answer"},
		{[]string{"--lane", "full", "q", "--refresh"}, []string{"q"}, true, "full"},
		{[]string{"q", "--lane=small"}, []string{"q"}, false, "small"},
		{[]string{"--", "-leading-dash"}, []string{"-leading-dash"}, false, "answer"},
		{[]string{"--refresh", "--", "--refresh"}, []string{"--refresh"}, true, "answer"},
		{[]string{"a", "b"}, []string{"a", "b"}, false, "answer"},
	}
	for _, c := range cases {
		fs := flag.NewFlagSet("t", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		r := fs.Bool("refresh", false, "")
		l := fs.String("lane", "answer", "")
		got, err := parseInterspersed(fs, c.args)
		if err != nil || !reflect.DeepEqual(got, c.want) || *r != c.refresh || *l != c.lane {
			t.Errorf("%v: got=%v err=%v refresh=%v lane=%v", c.args, got, err, *r, *l)
		}
	}
}
