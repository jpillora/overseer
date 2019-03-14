package example

import (
	"fmt"
	"net/http"
	"time"

	"github.com/jpillora/overseer"
)

//convert your 'main()' into a 'prog(state)'
//'prog()' is run in a child process
func Prog(BuildID string) func(state overseer.State) {
	return func(state overseer.State) {
		fmt.Printf("app#%s (%s) listening...\n", BuildID, state.ID)
		http.Handle("/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			d, _ := time.ParseDuration(r.URL.Query().Get("d"))
			time.Sleep(d)
			fmt.Fprintf(w, "app#%s (%s) says hello\n", BuildID, state.ID)
		}))
		http.Serve(state.Listener, nil)
		fmt.Printf("app#%s (%s) exiting...\n", BuildID, state.ID)
	}
}
