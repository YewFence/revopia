package main

import "github.com/yewfence/volume-backup/cmd"

var version = "dev"

func main() {
	cmd.SetVersion(version)
	cmd.Execute()
}
