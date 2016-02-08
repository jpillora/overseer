package main

import (
	"strconv"
	"time"

	"github.com/jpillora/go-upgrade"
	"github.com/jpillora/go-upgrade/fetcher"
	"github.com/jpillora/opts"
)

func main() {
	c := struct {
		URL  string `type:"arg" help:"<url> of where to GET the binary"`
		Port int    `help:"listening port"`
		Log  bool   `help:"enable logging"`
	}{
		Port: 3000,
		Log:  true,
	}
	opts.Parse(&c)
	upgrade.Run(upgrade.Config{
		Log: c.Log,
		Program: func(state upgrade.State) {
			//noop
			select {}
		},
		Address: ":" + strconv.Itoa(c.Port),
		Fetcher: &fetcher.HTTP{
			URL:      c.URL,
			Interval: 1 * time.Second,
		},
	})
}
