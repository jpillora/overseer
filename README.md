# go-upgrade

Self-upgrading binaries in Go (Golang)

:warning: This is beta software

### Install

```
go get github.com/jpillora/go-upgrade
```

### Quick Usage

``` go
package main

import (
	"log"
	"os"
	"time"

	"github.com/jpillora/go-upgrade"
)

var VERSION = "0.0.0" //set with ldflags

//change your 'main' into a 'prog'
func prog() {
	log.Printf("Running version %s...", VERSION)
	select {}
}

//then create another 'main' which runs the upgrades
func main() {
	upgrade.Run(upgrade.Config{
		Program: prog,
		Version: VERSION,
		Fetcher: upgrade.BasicFetcher(
			"http://localhost:3000/myapp_{{.Version}}",
		),
		FetchInterval: 2 * time.Hour,
		Signal:        os.Interrupt,
	})
}
```

### How it works

* `go-upgrade` uses the main process to check for and install upgrades and a child process to run `Program`
* If the current binary cannot be found or written to, `go-upgrade` will be disabled and `Program` will run in the main process
* On load `Program` will be run in a child process
* All standard pipes are connected to the child process
* All signals received are forwarded through to the child process
* Every `CheckInterval` the fetcher's `Fetch` method is called with the current version
* The `BasicFetcher` requires a URL with a version placeholder. On `Fetch`, the current version will be incremented and result URL will be requested (raw bytes, `.tar.gz`, `.gz`, `.zip` binary releases are supported), if successful, the binary is returned.
* When the binary is returned, its version is tested and checked against the current version, if it differs the upgrade is considered successful and the desired `Signal` will be sent to the child process.
* When the child process exits, the main process will exit with the same code (except for upgrade restarts).
* Upgrade restarts are performed once after an upgrade - any subsequence exits will also cause the main process to exit - **so `go-upgrade` is not a process manager**.


### Fetchers

#### Basic

**Performs a simple web request at the desired URL**

When performing the version increment step, the current version will be parsed and each of the numerical sections will be grouped. One at a time, from right to left, each will be incremented and the new URL will requested. So, in the example above, `0.5.1` will be tried, then `0.6.0`, then `1.5.0`. Numerical semantic versions aren't required, you could also simply have `v1`, which then would be incremented to `v2`.

#### Github

**Uses Github releases to locate and download the newest version**

*TODO*

### Alternatives

* https://github.com/sanbornm/go-selfupdate
* https://github.com/inconshreveable/go-update

### Todo

* Delta updates with https://github.com/kr/binarydist
* Signed binaries (in the meantime, use HTTPS where possible)

#### MIT License

Copyright © 2015 Jaime Pillora &lt;dev@jpillora.com&gt;

Permission is hereby granted, free of charge, to any person obtaining
a copy of this software and associated documentation files (the
'Software'), to deal in the Software without restriction, including
without limitation the rights to use, copy, modify, merge, publish,
distribute, sublicense, and/or sell copies of the Software, and to
permit persons to whom the Software is furnished to do so, subject to
the following conditions:

The above copyright notice and this permission notice shall be
included in all copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED 'AS IS', WITHOUT WARRANTY OF ANY KIND,
EXPRESS OR IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF
MERCHANTABILITY, FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT.
IN NO EVENT SHALL THE AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY
CLAIM, DAMAGES OR OTHER LIABILITY, WHETHER IN AN ACTION OF CONTRACT,
TORT OR OTHERWISE, ARISING FROM, OUT OF OR IN CONNECTION WITH THE
SOFTWARE OR THE USE OR OTHER DEALINGS IN THE SOFTWARE.
