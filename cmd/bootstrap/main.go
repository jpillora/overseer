package main

import (
	"strconv"
	"time"

	"github.com/jpillora/opts"
	"github.com/jpillora/overseer"
	"github.com/jpillora/overseer/fetcher"
)

func main() {
	c := struct {
		URL     string `type:"arg" help:"<url> of where to GET the binary"`
		Port    int    `help:"listening port"`
		NoDebug bool   `help:"disable debug mode"`
	}{
		Port: 3000,
	}
	opts.Parse(&c)
	overseer.Run(overseer.Config{
		Program: func(state overseer.State) {
			//block forever
			select {}
		},
		Address: ":" + strconv.Itoa(c.Port),
		Fetcher: &fetcher.HTTP{
			URL:      c.URL,
			Interval: 1 * time.Second,
		},
		Debug: !c.NoDebug,
	})
}
