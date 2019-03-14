package main

import (
	"github.com/jpillora/overseer"
	"github.com/jpillora/overseer/example"
	"github.com/jpillora/overseer/fetcher"
)

//see ../example.sh default for the use-case

// BuildID is compile-time variable
var BuildID = "0"

//then create another 'main' which runs the upgrades
//'main()' is run in the initial process
func main() {
	overseer.Run(overseer.Config{
		Program: example.Prog(BuildID),
		Address: ":5001",
		Fetcher: &fetcher.File{Path: "my_app_next"},
		Debug:   false, //display log of overseer actions
	})
}
