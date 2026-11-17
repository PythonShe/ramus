package commitgraph

import "runtime"

// charSigned is whether C's char is signed where git was built, taken
// to be this machine: git's version 1 hash reads path bytes as char, so
// a graph written on Linux on ARM, POWER, s390x or RISC-V, where char is
// unsigned, hashes bytes above 0x7f as version 2 does. Apple's and
// Microsoft's ABIs keep char signed on ARM.
var charSigned = func() bool {
	switch runtime.GOARCH {
	case "arm", "arm64", "ppc64", "ppc64le", "s390x", "riscv64":
		return runtime.GOOS == "darwin" || runtime.GOOS == "ios" || runtime.GOOS == "windows"
	}
	return true
}()
