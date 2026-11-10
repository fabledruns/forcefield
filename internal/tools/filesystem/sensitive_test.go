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
		// NTFS Alternate Data Streams (ADS) regression tests
		{".env::$DATA", true},
		{".env:stream", true},
		{".env:stream:$DATA", true},
		{".env.local::$DATA", true},
		{"subdir/.env::$DATA", true},
		{"subdir\\.env::$DATA", true},
		{"C:\\project\\.env::$DATA", true},
		{"C:.env::$DATA", true},
		{"id_rsa::$DATA", true},
		{"id_rsa:stream", true},
		{"id_ed25519::$DATA", true},
		{"cert.pem::$DATA", true},
		{"server.key::$DATA", true},
		{"server.key:stream", true},
		{".ssh::$DATA", true},
		{".ssh/id_rsa::$DATA", true},
		{".aws/credentials::$DATA", true},
		{".kube/config::$DATA", true},
		{".netrc::$DATA", true},
		{".git-credentials::$DATA", true},
		// ADS negative cases (normal files remain non-sensitive)
		{"normal.txt::$DATA", false},
		{"README.md:stream", false},
		{"src/main.go::$DATA", false},
		{"config.yaml::$DATA", false},
		{"C:\\project\\main.go::$DATA", false},
	}
	for _, tc := range cases {
		if got := IsSensitivePath(tc.path); got != tc.want {
			t.Errorf("IsSensitivePath(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestIsSensitivePath_WindowsNTFS(t *testing.T) {
	windowsCases := []struct {
		name string
		path string
		want bool
	}{
		{"EnvDefaultStream", `C:\Users\Admin\project\.env::$DATA`, true},
		{"EnvCustomStream", `C:\Users\Admin\project\.env:hidden`, true},
		{"EnvCustomStreamData", `C:\Users\Admin\project\.env:hidden:$DATA`, true},
		{"EnvLocalStream", `D:\work\.env.production::$DATA`, true},
		{"SshIdRsaStream", `C:\Users\Admin\.ssh\id_rsa::$DATA`, true},
		{"SshDirStream", `C:\Users\Admin\.ssh::$DATA`, true},
		{"KeyStream", `E:\certs\private.key::$DATA`, true},
		{"PemStream", `E:\certs\cert.pem::$DATA`, true},
		{"AwsCredsStream", `C:\Users\Admin\.aws\credentials::$DATA`, true},
		{"KubeConfigStream", `C:\Users\Admin\.kube\config::$DATA`, true},
		{"NetrcStream", `C:\Users\Admin\.netrc::$DATA`, true},
		{"NormalFileStream", `C:\Users\Admin\project\main.go::$DATA`, false},
		{"NormalDocStream", `C:\Users\Admin\project\README.md:summary`, false},
	}
	for _, tc := range windowsCases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSensitivePath(tc.path); got != tc.want {
				t.Errorf("IsSensitivePath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}
