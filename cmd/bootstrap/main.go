package main

import (
	"strconv"
	"time"

	"github.com/jpillora/overseer"
	"github.com/jpillora/overseer/fetcher"
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
	overseer.Run(overseer.Config{
		Log: c.Log,
		Program: func(state overseer.State) {
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
