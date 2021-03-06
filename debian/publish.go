package debian

import (
	"bufio"
	"bytes"
	"code.google.com/p/go-uuid/uuid"
	"fmt"
	"github.com/smira/aptly/aptly"
	"github.com/smira/aptly/database"
	"github.com/smira/aptly/utils"
	"github.com/ugorji/go/codec"
	"log"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// PublishedRepo is a published for http/ftp representation of snapshot as Debian repository
type PublishedRepo struct {
	// Internal unique ID
	UUID string
	// Prefix & distribution should be unique across all published repositories
	Prefix       string
	Distribution string
	Component    string
	// Architectures is a list of all architectures published
	Architectures []string
	// SourceKind is "local"/"repo"
	SourceKind string
	// SourceUUID is UUID of either snapshot or local repo
	SourceUUID string `codec:"SnapshotUUID"`

	snapshot  *Snapshot
	localRepo *LocalRepo
}

// NewPublishedRepo creates new published repository
//
// prefix specifies publishing prefix
// distribution, component and architectures are user-defined properties
// source could either be *Snapshot or *LocalRepo
func NewPublishedRepo(prefix string, distribution string, component string, architectures []string, source interface{}, collectionFactory *CollectionFactory) (*PublishedRepo, error) {
	var ok bool

	result := &PublishedRepo{
		UUID:          uuid.New(),
		Architectures: architectures,
	}

	// figure out source
	result.snapshot, ok = source.(*Snapshot)
	if ok {
		result.SourceKind = "snapshot"
		result.SourceUUID = result.snapshot.UUID
	} else {
		result.localRepo, ok = source.(*LocalRepo)
		if ok {
			result.SourceKind = "local"
			result.SourceUUID = result.localRepo.UUID
		} else {
			panic("unknown source kind")
		}
	}

	// clean & verify prefix
	prefix = filepath.Clean(prefix)
	if strings.HasPrefix(prefix, "/") {
		prefix = prefix[1:]
	}
	if strings.HasSuffix(prefix, "/") {
		prefix = prefix[:len(prefix)-1]
	}
	prefix = filepath.Clean(prefix)

	for _, part := range strings.Split(prefix, "/") {
		if part == ".." || part == "dists" || part == "pool" {
			return nil, fmt.Errorf("invalid prefix %s", prefix)
		}
	}

	result.Prefix = prefix

	// guessing distribution & component
	if component == "" || distribution == "" {
		var (
			head              interface{}
			current           = []interface{}{source}
			rootComponents    = []string{}
			rootDistributions = []string{}
		)

		// walk up the tree from current source up to roots (local or remote repos)
		// and collect information about distribution and components
		for len(current) > 0 {
			head, current = current[0], current[1:]

			if snapshot, ok := head.(*Snapshot); ok {
				for _, uuid := range snapshot.SourceIDs {
					if snapshot.SourceKind == "repo" {
						remoteRepo, err := collectionFactory.RemoteRepoCollection().ByUUID(uuid)
						if err != nil {
							continue
						}
						current = append(current, remoteRepo)
					} else if snapshot.SourceKind == "local" {
						localRepo, err := collectionFactory.LocalRepoCollection().ByUUID(uuid)
						if err != nil {
							continue
						}
						current = append(current, localRepo)
					} else if snapshot.SourceKind == "snapshot" {
						snap, err := collectionFactory.SnapshotCollection().ByUUID(uuid)
						if err != nil {
							continue
						}
						current = append(current, snap)
					}
				}
			} else if localRepo, ok := head.(*LocalRepo); ok {
				if localRepo.DefaultDistribution != "" {
					rootDistributions = append(rootDistributions, localRepo.DefaultDistribution)
				}
				if localRepo.DefaultComponent != "" {
					rootComponents = append(rootComponents, localRepo.DefaultComponent)
				}
			} else if remoteRepo, ok := head.(*RemoteRepo); ok {
				if remoteRepo.Distribution != "" {
					rootDistributions = append(rootDistributions, remoteRepo.Distribution)
				}
				rootComponents = append(rootComponents, remoteRepo.Components...)
			} else {
				panic("unknown type")
			}
		}

		if distribution == "" {
			sort.Strings(rootDistributions)
			if len(rootDistributions) > 0 && rootDistributions[0] == rootDistributions[len(rootDistributions)-1] {
				distribution = rootDistributions[0]
			} else {
				return nil, fmt.Errorf("unable to guess distribution name, please specify explicitly")
			}
		}

		if component == "" {
			sort.Strings(rootComponents)
			if len(rootComponents) > 0 && rootComponents[0] == rootComponents[len(rootComponents)-1] {
				component = rootComponents[0]
			} else {
				component = "main"
			}
		}
	}

	result.Distribution, result.Component = distribution, component

	return result, nil
}

// String returns human-readable represenation of PublishedRepo
func (p *PublishedRepo) String() string {
	var source string

	if p.snapshot != nil {
		source = p.snapshot.String()
	} else if p.localRepo != nil {
		source = p.localRepo.String()
	} else {
		panic("no snapshot/localRepo")
	}

	return fmt.Sprintf("%s/%s (%s) [%s] publishes %s", p.Prefix, p.Distribution, p.Component, strings.Join(p.Architectures, ", "), source)
}

// Key returns unique key identifying PublishedRepo
func (p *PublishedRepo) Key() []byte {
	return []byte("U" + p.Prefix + ">>" + p.Distribution)
}

// Encode does msgpack encoding of PublishedRepo
func (p *PublishedRepo) Encode() []byte {
	var buf bytes.Buffer

	encoder := codec.NewEncoder(&buf, &codec.MsgpackHandle{})
	encoder.Encode(p)

	return buf.Bytes()
}

// Decode decodes msgpack representation into PublishedRepo
func (p *PublishedRepo) Decode(input []byte) error {
	decoder := codec.NewDecoderBytes(input, &codec.MsgpackHandle{})
	err := decoder.Decode(p)
	if err != nil {
		return err
	}

	// old PublishedRepo were publishing only snapshots
	if p.SourceKind == "" {
		p.SourceKind = "snapshot"
	}

	return nil
}

// Publish publishes snapshot (repository) contents, links package files, generates Packages & Release files, signs them
func (p *PublishedRepo) Publish(packagePool aptly.PackagePool, publishedStorage aptly.PublishedStorage, collectionFactory *CollectionFactory, signer utils.Signer, progress aptly.Progress) error {
	err := publishedStorage.MkDir(filepath.Join(p.Prefix, "pool"))
	if err != nil {
		return err
	}
	basePath := filepath.Join(p.Prefix, "dists", p.Distribution)
	err = publishedStorage.MkDir(basePath)
	if err != nil {
		return err
	}

	if progress != nil {
		progress.Printf("Loading packages...\n")
	}

	var refList *PackageRefList

	if p.snapshot != nil {
		refList = p.snapshot.RefList()
	} else if p.localRepo != nil {
		refList = p.localRepo.RefList()
	} else {
		panic("no source")
	}

	// Load all packages
	list, err := NewPackageListFromRefList(refList, collectionFactory.PackageCollection(), progress)
	if err != nil {
		return fmt.Errorf("unable to load packages: %s", err)
	}

	if list.Len() == 0 {
		return fmt.Errorf("snapshot is empty")
	}

	if len(p.Architectures) == 0 {
		p.Architectures = list.Architectures(true)
	}

	if len(p.Architectures) == 0 {
		return fmt.Errorf("unable to figure out list of architectures, please supply explicit list")
	}

	sort.Strings(p.Architectures)

	generatedFiles := map[string]utils.ChecksumInfo{}

	if progress != nil {
		progress.Printf("Generating metadata files and linking package files...\n")
	}

	// For all architectures, generate release file
	for _, arch := range p.Architectures {
		if progress != nil {
			progress.InitBar(int64(list.Len()), false)
		}

		var relativePath string
		if arch == "source" {
			relativePath = filepath.Join(p.Component, "source", "Sources")
		} else {
			relativePath = filepath.Join(p.Component, fmt.Sprintf("binary-%s", arch), "Packages")
		}
		err = publishedStorage.MkDir(filepath.Dir(filepath.Join(basePath, relativePath)))
		if err != nil {
			return err
		}

		packagesFile, err := publishedStorage.CreateFile(filepath.Join(basePath, relativePath))
		if err != nil {
			return fmt.Errorf("unable to creates Packages file: %s", err)
		}

		bufWriter := bufio.NewWriter(packagesFile)

		err = list.ForEach(func(pkg *Package) error {
			if progress != nil {
				progress.AddBar(1)
			}
			if pkg.MatchesArchitecture(arch) {
				err = pkg.LinkFromPool(publishedStorage, packagePool, p.Prefix, p.Component)
				if err != nil {
					return err
				}

				err = pkg.Stanza().WriteTo(bufWriter)
				if err != nil {
					return err
				}
				err = bufWriter.WriteByte('\n')
				if err != nil {
					return err
				}

				pkg.files = nil
				pkg.deps = nil
				pkg.extra = nil

			}

			return nil
		})

		if err != nil {
			return fmt.Errorf("unable to process packages: %s", err)
		}

		err = bufWriter.Flush()
		if err != nil {
			return fmt.Errorf("unable to write Packages file: %s", err)
		}

		err = utils.CompressFile(packagesFile)
		if err != nil {
			return fmt.Errorf("unable to compress Packages files: %s", err)
		}

		packagesFile.Close()

		checksumInfo, err := publishedStorage.ChecksumsForFile(filepath.Join(basePath, relativePath))
		if err != nil {
			return fmt.Errorf("unable to collect checksums: %s", err)
		}
		generatedFiles[relativePath] = checksumInfo

		checksumInfo, err = publishedStorage.ChecksumsForFile(filepath.Join(basePath, relativePath+".gz"))
		if err != nil {
			return fmt.Errorf("unable to collect checksums: %s", err)
		}
		generatedFiles[relativePath+".gz"] = checksumInfo

		checksumInfo, err = publishedStorage.ChecksumsForFile(filepath.Join(basePath, relativePath+".bz2"))
		if err != nil {
			return fmt.Errorf("unable to collect checksums: %s", err)
		}
		generatedFiles[relativePath+".bz2"] = checksumInfo

		if progress != nil {
			progress.ShutdownBar()
		}
	}

	release := make(Stanza)
	release["Origin"] = p.Prefix + " " + p.Distribution
	release["Label"] = p.Prefix + " " + p.Distribution
	release["Codename"] = p.Distribution
	release["Date"] = time.Now().UTC().Format("Mon, 2 Jan 2006 15:04:05 MST")
	release["Components"] = p.Component
	release["Architectures"] = strings.Join(utils.StrSlicesSubstract(p.Architectures, []string{"source"}), " ")
	release["Description"] = " Generated by aptly\n"
	release["MD5Sum"] = "\n"
	release["SHA1"] = "\n"
	release["SHA256"] = "\n"

	for path, info := range generatedFiles {
		release["MD5Sum"] += fmt.Sprintf(" %s %8d %s\n", info.MD5, info.Size, path)
		release["SHA1"] += fmt.Sprintf(" %s %8d %s\n", info.SHA1, info.Size, path)
		release["SHA256"] += fmt.Sprintf(" %s %8d %s\n", info.SHA256, info.Size, path)
	}

	releaseFile, err := publishedStorage.CreateFile(filepath.Join(basePath, "Release"))
	if err != nil {
		return fmt.Errorf("unable to create Release file: %s", err)
	}

	bufWriter := bufio.NewWriter(releaseFile)

	err = release.WriteTo(bufWriter)
	if err != nil {
		return fmt.Errorf("unable to create Release file: %s", err)
	}

	err = bufWriter.Flush()
	if err != nil {
		return fmt.Errorf("unable to create Release file: %s", err)
	}

	releaseFilename := releaseFile.Name()
	releaseFile.Close()

	// Signing files might output to console, so flush progress writer first
	if progress != nil {
		progress.Flush()
	}

	if signer != nil {
		err = signer.DetachedSign(releaseFilename, releaseFilename+".gpg")
		if err != nil {
			return fmt.Errorf("unable to sign Release file: %s", err)
		}

		err = signer.ClearSign(releaseFilename, filepath.Join(filepath.Dir(releaseFilename), "InRelease"))
		if err != nil {
			return fmt.Errorf("unable to sign Release file: %s", err)
		}
	}

	return nil
}

// RemoveFiles removes files that were created by Publish
//
// It can remove prefix fully, and part of pool (for specific component)
func (p *PublishedRepo) RemoveFiles(publishedStorage aptly.PublishedStorage, removePrefix, removePoolComponent bool) error {
	if removePrefix {
		err := publishedStorage.RemoveDirs(filepath.Join(p.Prefix, "dists"))
		if err != nil {
			return err
		}

		return publishedStorage.RemoveDirs(filepath.Join(p.Prefix, "pool"))
	}

	err := publishedStorage.RemoveDirs(filepath.Join(p.Prefix, "dists", p.Distribution))
	if err != nil {
		return err
	}

	if removePoolComponent {
		err = publishedStorage.RemoveDirs(filepath.Join(p.Prefix, "pool", p.Component))
		if err != nil {
			return err
		}
	}
	return nil
}

// PublishedRepoCollection does listing, updating/adding/deleting of PublishedRepos
type PublishedRepoCollection struct {
	db   database.Storage
	list []*PublishedRepo
}

// NewPublishedRepoCollection loads PublishedRepos from DB and makes up collection
func NewPublishedRepoCollection(db database.Storage) *PublishedRepoCollection {
	result := &PublishedRepoCollection{
		db: db,
	}

	blobs := db.FetchByPrefix([]byte("U"))
	result.list = make([]*PublishedRepo, 0, len(blobs))

	for _, blob := range blobs {
		r := &PublishedRepo{}
		if err := r.Decode(blob); err != nil {
			log.Printf("Error decoding published repo: %s\n", err)
		} else {
			result.list = append(result.list, r)
		}
	}

	return result
}

// Add appends new repo to collection and saves it
func (collection *PublishedRepoCollection) Add(repo *PublishedRepo) error {
	if collection.CheckDuplicate(repo) != nil {
		return fmt.Errorf("published repo with prefix/distribution %s/%s already exists", repo.Prefix, repo.Distribution)
	}

	err := collection.Update(repo)
	if err != nil {
		return err
	}

	collection.list = append(collection.list, repo)
	return nil
}

// CheckDuplicate verifies that there's no published repo with the same name
func (collection *PublishedRepoCollection) CheckDuplicate(repo *PublishedRepo) *PublishedRepo {
	for _, r := range collection.list {
		if r.Prefix == repo.Prefix && r.Distribution == repo.Distribution {
			return r
		}
	}

	return nil
}

// Update stores updated information about repo in DB
func (collection *PublishedRepoCollection) Update(repo *PublishedRepo) error {
	err := collection.db.Put(repo.Key(), repo.Encode())
	if err != nil {
		return err
	}
	return nil
}

// LoadComplete loads additional information for remote repo
func (collection *PublishedRepoCollection) LoadComplete(repo *PublishedRepo, collectionFactory *CollectionFactory) error {
	var err error

	if repo.SourceKind == "snapshot" {
		repo.snapshot, err = collectionFactory.SnapshotCollection().ByUUID(repo.SourceUUID)
	} else if repo.SourceKind == "local" {
		repo.localRepo, err = collectionFactory.LocalRepoCollection().ByUUID(repo.SourceUUID)
	} else {
		panic("unknown SourceKind")
	}

	return err
}

// ByPrefixDistribution looks up repository by prefix & distribution
func (collection *PublishedRepoCollection) ByPrefixDistribution(prefix, distribution string) (*PublishedRepo, error) {
	for _, r := range collection.list {
		if r.Prefix == prefix && r.Distribution == distribution {
			return r, nil
		}
	}
	return nil, fmt.Errorf("published repo with prefix/distribution %s/%s not found", prefix, distribution)
}

// ByUUID looks up repository by uuid
func (collection *PublishedRepoCollection) ByUUID(uuid string) (*PublishedRepo, error) {
	for _, r := range collection.list {
		if r.UUID == uuid {
			return r, nil
		}
	}
	return nil, fmt.Errorf("published repo with uuid %s not found", uuid)
}

// BySnapshot looks up repository by snapshot source
func (collection *PublishedRepoCollection) BySnapshot(snapshot *Snapshot) []*PublishedRepo {
	result := make([]*PublishedRepo, 0)
	for _, r := range collection.list {
		if r.SourceKind == "snapshot" && r.SourceUUID == snapshot.UUID {
			result = append(result, r)
		}
	}
	return result
}

// ByLocalRepo looks up repository by local repo source
func (collection *PublishedRepoCollection) ByLocalRepo(repo *LocalRepo) []*PublishedRepo {
	result := make([]*PublishedRepo, 0)
	for _, r := range collection.list {
		if r.SourceKind == "local" && r.SourceUUID == repo.UUID {
			result = append(result, r)
		}
	}
	return result
}

// ForEach runs method for each repository
func (collection *PublishedRepoCollection) ForEach(handler func(*PublishedRepo) error) error {
	var err error
	for _, r := range collection.list {
		err = handler(r)
		if err != nil {
			return err
		}
	}
	return err
}

// Len returns number of remote repos
func (collection *PublishedRepoCollection) Len() int {
	return len(collection.list)
}

// Remove removes published repository, cleaning up directories, files
func (collection *PublishedRepoCollection) Remove(publishedStorage aptly.PublishedStorage, prefix, distribution string) error {
	repo, err := collection.ByPrefixDistribution(prefix, distribution)
	if err != nil {
		return err
	}

	removePrefix := true
	removePoolComponent := true
	repoPosition := -1

	for i, r := range collection.list {
		if r == repo {
			repoPosition = i
			continue
		}
		if r.Prefix == repo.Prefix {
			removePrefix = false
			if r.Component == repo.Component {
				removePoolComponent = false
			}
		}
	}

	err = repo.RemoveFiles(publishedStorage, removePrefix, removePoolComponent)
	if err != nil {
		return err
	}

	collection.list[len(collection.list)-1], collection.list[repoPosition], collection.list =
		nil, collection.list[len(collection.list)-1], collection.list[:len(collection.list)-1]

	return collection.db.Delete(repo.Key())
}
