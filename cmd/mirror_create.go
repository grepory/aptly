package cmd

import (
	"fmt"
	"github.com/gonuts/commander"
	"github.com/gonuts/flag"
	"github.com/smira/aptly/debian"
	"github.com/smira/aptly/utils"
	"strings"
)

func aptlyMirrorCreate(cmd *commander.Command, args []string) error {
	var err error
	if !(len(args) == 2 && strings.HasPrefix(args[1], "ppa:") || len(args) >= 3) {
		cmd.Usage()
		return err
	}

	downloadSources := utils.Config.DownloadSourcePackages || cmd.Flag.Lookup("with-sources").Value.Get().(bool)

	var (
		mirrorName, archiveURL, distribution string
		components                           []string
	)

	mirrorName = args[0]
	if len(args) == 2 {
		archiveURL, distribution, components, err = debian.ParsePPA(args[1])
		if err != nil {
			return err
		}
	} else {
		archiveURL, distribution, components = args[1], args[2], args[3:]
	}

	repo, err := debian.NewRemoteRepo(mirrorName, archiveURL, distribution, components, context.architecturesList, downloadSources)
	if err != nil {
		return fmt.Errorf("unable to create mirror: %s", err)
	}

	verifier, err := getVerifier(cmd)
	if err != nil {
		return fmt.Errorf("unable to initialize GPG verifier: %s", err)
	}

	err = repo.Fetch(context.downloader, verifier)
	if err != nil {
		return fmt.Errorf("unable to fetch mirror: %s", err)
	}

	repoCollection := debian.NewRemoteRepoCollection(context.database)

	err = repoCollection.Add(repo)
	if err != nil {
		return fmt.Errorf("unable to add mirror: %s", err)
	}

	fmt.Printf("\nMirror %s successfully added.\nYou can run 'aptly mirror update %s' to download repository contents.\n", repo, repo.Name)
	return err
}

func makeCmdMirrorCreate() *commander.Command {
	cmd := &commander.Command{
		Run:       aptlyMirrorCreate,
		UsageLine: "create <name> <archive url> <distribution> [<component1> ...]",
		Short:     "create new mirror",
		Long: `
Creates mirror <name> of remote repository, aptly supports both regular and flat Debian repositories exported
via HTTP. aptly would try download Release file from remote repository and verify its signature.

PPA urls could specified in short format:

  $ aptly mirror create <name> ppa:<user>/<project>

Example:

  $ aptly mirror create wheezy-main http://mirror.yandex.ru/debian/ wheezy main
`,
		Flag: *flag.NewFlagSet("aptly-mirror-create", flag.ExitOnError),
	}

	cmd.Flag.Bool("ignore-signatures", false, "disable verification of Release file signatures")
	cmd.Flag.Bool("with-sources", false, "download source packages in addition to binary packages")
	cmd.Flag.Var(&keyRings, "keyring", "gpg keyring to use when verifying Release file (could be specified multiple times)")

	return cmd
}
