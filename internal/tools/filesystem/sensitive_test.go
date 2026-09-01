package filesystem

import "testing"

func TestIsSensitivePath(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{".env", true},
		{".env.local", true},
		{".env.production", true},
		{"subdir/.env", true},
		{"/tmp/.env", true},
		{"my.pem", true},
		{"cert.PEM", true},
		{"secret.key", true},
		{"secret.KEY", true},
		{".ssh/id_rsa", true},
		{"/home/user/.ssh/id_rsa", true},
		{"/home/user/.ssh/known_hosts", true},
		{".aws/credentials", true},
		{"/home/user/.aws/config", true},
		{".kube/config", true},
		{".gcloud/credentials", true},
		{".docker/config.json", true},
		{".netrc", true},
		{".git-credentials", true},
		{"normal.txt", false},
		{"README.md", false},
		{"src/main.go", false},
		{"config.yaml", false},
		{"some/dir/file.json", false},
		{"", false},
		{".env.example", true}, // still .env.* -> conservative true
	}
	for _, tc := range cases {
		if got := IsSensitivePath(tc.path); got != tc.want {
			t.Errorf("IsSensitivePath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}
