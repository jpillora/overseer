
:warning: This project is currently documentation only. There are [other](https://github.com/sanbornm/go-selfupdate) [projects](https://github.com/inconshreveable/go-update) for upgrading Go programs though they seem to place too many restrictions on how and when upgrades are performed.

---

# go-upgrade

Upgrade the binaries of running Go (Golang) programs

### Install

```
go get ...
```

### Usage

``` go
package main

var VERSION = "0.5.0" //set with ldflags

func main() {
	u := upgrade.New(upgrade.Config{
		Version: VERSION,
		URL: "https://example.com/build/prog.{{ .Version }}.tar"
	})

	if upgraded, err := u.Upgrade(); err != nil {
		log.Print(err) //update process failed
	} else if !upgraded {
		//no updates found
	} else {
		//updated succeeded, your binary has been swapped, you can now:
		//1. continue execution, and the new version will be used on next run
		//2. os.Exit(0) and force user to restart
		//3. os.Exit(1) and allow the init system perform a restart
	}

	log.Printf("Running version %s...", VERSION)
	select {}
}
```

When doing an `Upgrade()`, the `Version` will be parsed and each of the numerical sections will be parsed. One at a time, from right to left, each will be incremented and the new URL will requested. So, in the example above, `0.5.1` will be tried, then `0.6.0`, then `1.5.0`. Numerical semantic versions aren't required, you could also simply have `v1`, which then would be incremented to `v2`.

### Known issues

* Your new binary panics before the upgrade process and you're stuck with a broken version. This could possibly be resolved with `recover`.

### Todo

* Delta updates with https://github.com/kr/binarydist

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
