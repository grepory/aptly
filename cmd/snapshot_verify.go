package cmd

import (
	"fmt"
	"github.com/gonuts/commander"
	"github.com/gonuts/flag"
	"github.com/smira/aptly/debian"
	"sort"
)

func aptlySnapshotVerify(cmd *commander.Command, args []string) error {
	var err error
	if len(args) < 1 {
		cmd.Usage()
		return err
	}

	snapshotCollection := debian.NewSnapshotCollection(context.database)
	packageCollection := debian.NewPackageCollection(context.database)

	snapshots := make([]*debian.Snapshot, len(args))
	for i := range snapshots {
		snapshots[i], err = snapshotCollection.ByName(args[i])
		if err != nil {
			return fmt.Errorf("unable to verify: %s", err)
		}

		err = snapshotCollection.LoadComplete(snapshots[i])
		if err != nil {
			return fmt.Errorf("unable to verify: %s", err)
		}
	}

	context.progress.Printf("Loading packages...\n")

	packageList, err := debian.NewPackageListFromRefList(snapshots[0].RefList(), packageCollection, context.progress)
	if err != nil {
		fmt.Errorf("unable to load packages: %s", err)
	}

	sourcePackageList := debian.NewPackageList()
	err = sourcePackageList.Append(packageList)
	if err != nil {
		fmt.Errorf("unable to merge sources: %s", err)
	}

	for i := 1; i < len(snapshots); i++ {
		pL, err := debian.NewPackageListFromRefList(snapshots[i].RefList(), packageCollection, context.progress)
		if err != nil {
			fmt.Errorf("unable to load packages: %s", err)
		}

		err = sourcePackageList.Append(pL)
		if err != nil {
			fmt.Errorf("unable to merge sources: %s", err)
		}
	}

	sourcePackageList.PrepareIndex()

	var architecturesList []string

	if len(context.architecturesList) > 0 {
		architecturesList = context.architecturesList
	} else {
		architecturesList = packageList.Architectures(true)
	}

	if len(architecturesList) == 0 {
		return fmt.Errorf("unable to determine list of architectures, please specify explicitly")
	}

	context.progress.Printf("Verifying...\n")

	missing, err := packageList.VerifyDependencies(context.dependencyOptions, architecturesList, sourcePackageList, context.progress)
	if err != nil {
		return fmt.Errorf("unable to verify dependencies: %s", err)
	}

	if len(missing) == 0 {
		context.progress.Printf("All dependencies are satisfied.\n")
	} else {
		context.progress.Printf("Missing dependencies (%d):\n", len(missing))
		deps := make([]string, len(missing))
		i := 0
		for _, dep := range missing {
			deps[i] = dep.String()
			i++
		}

		sort.Strings(deps)

		for _, dep := range deps {
			context.progress.Printf("  %s\n", dep)
		}
	}

	return err
}

func makeCmdSnapshotVerify() *commander.Command {
	cmd := &commander.Command{
		Run:       aptlySnapshotVerify,
		UsageLine: "verify <name> [<source> ...]",
		Short:     "verify dependencies in snapshot",
		Long: `
Verify does depenency resolution in snapshot <name>, possibly using additional
snapshots <source> as dependency sources. All unsatisfied dependencies are
printed.

Example:

    $ aptly snapshot verify wheezy-main wheezy-contrib wheezy-non-free
`,
		Flag: *flag.NewFlagSet("aptly-snapshot-verify", flag.ExitOnError),
	}

	return cmd
}
