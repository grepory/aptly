package cmd

import (
	"fmt"
	"github.com/gonuts/commander"
	"github.com/gonuts/flag"
	"github.com/smira/aptly/debian"
	"github.com/smira/aptly/utils"
	"net"
	"net/http"
	"os"
	"sort"
)

func aptlyServe(cmd *commander.Command, args []string) error {
	var err error

	if context.collectionFactory.PublishedRepoCollection().Len() == 0 {
		fmt.Printf("No published repositories, unable to serve.\n")
		return nil
	}

	listen := cmd.Flag.Lookup("listen").Value.String()

	listenHost, listenPort, err := net.SplitHostPort(listen)

	if err != nil {
		return fmt.Errorf("wrong -listen specification: %s", err)
	}

	if listenHost == "" {
		listenHost, err = os.Hostname()
		if err != nil {
			listenHost = "localhost"
		}
	}

	fmt.Printf("Serving published repositories, recommended apt sources list:\n\n")

	sources := make(sort.StringSlice, 0, context.collectionFactory.PublishedRepoCollection().Len())
	published := make(map[string]*debian.PublishedRepo, context.collectionFactory.PublishedRepoCollection().Len())

	err = context.collectionFactory.PublishedRepoCollection().ForEach(func(repo *debian.PublishedRepo) error {
		err := context.collectionFactory.PublishedRepoCollection().LoadComplete(repo, context.collectionFactory)
		if err != nil {
			return err
		}

		sources = append(sources, repo.String())
		published[repo.String()] = repo

		return nil
	})

	if err != nil {
		return fmt.Errorf("unable to serve: %s", err)
	}

	sort.Strings(sources)

	for _, source := range sources {
		repo := published[source]

		prefix := repo.Prefix
		if prefix == "." {
			prefix = ""
		} else {
			prefix += "/"
		}

		fmt.Printf("# %s\ndeb http://%s:%s/%s %s %s\n",
			repo, listenHost, listenPort, prefix, repo.Distribution, repo.Component)

		if utils.StrSliceHasItem(repo.Architectures, "source") {
			fmt.Printf("deb-src http://%s:%s/%s %s %s\n",
				listenHost, listenPort, prefix, repo.Distribution, repo.Component)
		}
	}

	context.database.Close()

	fmt.Printf("\nStarting web server at: %s (press Ctrl+C to quit)...\n", listen)

	err = http.ListenAndServe(listen, http.FileServer(http.Dir(context.publishedStorage.PublicPath())))
	if err != nil {
		return fmt.Errorf("unable to serve: %s", err)
	}
	return nil
}

func makeCmdServe() *commander.Command {
	cmd := &commander.Command{
		Run:       aptlyServe,
		UsageLine: "serve",
		Short:     "HTTP serve published repositories",
		Long: `
Command serve starts embedded HTTP server (not suitable for real production usage) to serve
contents of public/ subdirectory of aptly's root that contains published repositories.

Example:

  $ aptly serve -listen=:8080
`,
		Flag: *flag.NewFlagSet("aptly-serve", flag.ExitOnError),
	}

	cmd.Flag.String("listen", ":8080", "host:port for HTTP listening")

	return cmd
}
