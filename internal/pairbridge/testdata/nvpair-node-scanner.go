// Fake nvpair-node-scanner: stays alive and consumes stdin; the bridge only
// needs it to hold the process open and accept discovery:register calls.
package main

import (
	"bufio"
	"os"
)

func main() {
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
	}
}
