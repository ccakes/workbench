package cli

import (
	"reflect"
	"testing"
)

func TestReorderFlags(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{
			name: "flags already first",
			args: []string{"-last", "200", "megalith"},
			want: []string{"-last", "200", "megalith"},
		},
		{
			name: "positional before flag",
			args: []string{"megalith", "-last", "200"},
			want: []string{"-last", "200", "megalith"},
		},
		{
			name: "positional between flags",
			args: []string{"-f", "megalith", "-last", "200"},
			want: []string{"-f", "megalith", "-last", "200"},
		},
		{
			name: "equals form",
			args: []string{"megalith", "-last=200"},
			want: []string{"-last=200", "megalith"},
		},
		{
			name: "no flags",
			args: []string{"megalith"},
			want: []string{"megalith"},
		},
		{
			name: "multiple positional after flags",
			args: []string{"-config", "bench.yaml", "svc1", "svc2"},
			want: []string{"-config", "bench.yaml", "svc1", "svc2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := reorderFlags(tt.args)
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("reorderFlags(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}
