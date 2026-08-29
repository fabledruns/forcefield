/*
Copyright © 2026 The Forcefield Creators

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

	http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
package main

import "forcefield/cmd"

// Version is set at build time via ldflags for backwards compatibility
// with Makefile's -X main.Version. Prefer forcefield/cmd.Version, but keep
// this var so older build invocations still embed a version.
var Version = "dev"

func main() {
	if Version != "dev" && cmd.Version == "dev" {
		cmd.Version = Version
	}
	cmd.Execute()
}
