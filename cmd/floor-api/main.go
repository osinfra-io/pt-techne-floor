// Command floor-api lists tofu-controller Terraform objects and approves
// their pending plans via server-side apply.
package main

import "log"

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

// run is filled in by Task 6.
func run() error {
	return nil
}
