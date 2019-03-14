package main

import (
	"github.com/jpillora/overseer"
	"github.com/jpillora/overseer/example"
	"github.com/jpillora/overseer/fetcher"
	"github.com/robfig/cron"
)

//see example.sh for the use-case

// BuildID is compile-time variable
var BuildID = "0"

//then create another 'main' which runs the upgrades
//'main()' is run in the initial process
func main() {
	Cron := cron.New()
	Cron.Start()
	defer Cron.Stop()

	schedule, _ := cron.Parse("@every 5s")

	overseer.Run(overseer.Config{
		Program: example.Prog(BuildID),
		Address: ":5001",
		Fetcher: &fetcher.File{Path: "my_app_next"},
		Debug:   false, //display log of overseer actions

		Cron:              Cron,
		FetchCronSchedule: &schedule,
	})
}
