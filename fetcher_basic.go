package upgrade

import (
	"bytes"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"text/template"
)

type basicFetcher struct {
	url      string
	urlTempl *template.Template
}

func BasicFetcher(url string) Fetcher {
	t := template.New("url")
	t, err := t.Parse(url)
	if err != nil {
		log.Fatalf("upgrade.BasicFetcher.url invalid: %s", err)
	}
	b := &basicFetcher{
		url:      url,
		urlTempl: t,
	}
	//test template
	b.getURL("0.1.0")
	return b
}

func (b *basicFetcher) getURL(version string) string {
	//run url template with this version
	var urlb bytes.Buffer
	if err := b.urlTempl.Execute(&urlb, struct {
		Version, OS, Arch string
	}{
		version, runtime.GOOS, runtime.GOARCH,
	}); err != nil {
		//execute will fail if theres a data error
		log.Fatalf("upgrade.BasicFetcher.url invalid: %s", err)
	}
	return urlb.String()
}

func (b *basicFetcher) Fetch(currentVersion string) ([]byte, error) {

	//get version permutations
	versions, err := getAllVersionIncrements(currentVersion)
	if err != nil {
		return nil, err
	}

	var bin []byte
	var errs []string
	//try all versions
	for _, v := range versions {
		url := b.getURL(v)
		resp, e := http.Get(url)
		if e != nil {
			errs = append(errs, "invalid request")
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode != 200 {
			errs = append(errs, v)
			continue
		}

		b, err := ReadAll(url, resp.Body)
		if err != nil {
			errs = append(errs, "download binary failed: "+err.Error())
			continue
		}

		//success!
		bin = b
		break
	}

	if bin != nil {
		return bin, nil
	}
	return nil, fmt.Errorf(strings.Join(errs, ", "))
}

func getAllVersionIncrements(version string) ([]string, error) {
	var versions []string
	curr := 0
	for {
		re := regexp.MustCompile(`\d+`)
		groups := re.FindAllString(version, -1)
		numGroups := len(groups)
		if numGroups == 0 {
			return nil, fmt.Errorf("No digits to increment in version: %s", version)
		}
		i := 0
		//we replace the version string numGroup times, swapping out one group at time
		v := re.ReplaceAllStringFunc(version, func(d string) string {
			l := len(d)
			//going from right to left
			if i == numGroups-1-curr {
				n, _ := strconv.Atoi(d)
				d = strconv.Itoa(n + 1)
			} else if i > numGroups-1-curr {
				//reset all numbers to the right to 0
				d = "0"
			}
			for len(d) < l {
				d = "0" + d
			}
			i++
			return d
		})
		versions = append(versions, v)
		curr++
		if curr == numGroups {
			break
		}
	}
	return versions, nil
}
