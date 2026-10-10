// Package landlock implements the Linux-only mechanism behind
// Forcefield's opt-in isolated execution mode: Landlock ABI query,
// ABI-masked filesystem rights, path-beneath rules, no_new_privs,
// and restriction inheritance across exec.
//
// The mechanism entered through a feasibility spike (re-exec test
// binary, policy pipe, grandchild probes) whose tests remain in this
// package as mechanism coverage: they exercise the same ApplyPolicy
// and policy codec the production helper path uses. The production
// entry point is sandbox.HelperMain (re-exec of the ff binary), not
// the spike child.
//
// Deliberately out of scope: network isolation, PID isolation,
// workspace .git/hooks protection (Landlock cannot deny a subpath of
// an allowed hierarchy), and cgroup/resource limits.
package landlock
