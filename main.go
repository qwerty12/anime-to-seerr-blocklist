package main

import (
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"codeberg.org/sdassow/atomic"
	"github.com/joho/godotenv"

	"anime-to-seerr-blocklist/internal/anime-list"
	"anime-to-seerr-blocklist/internal/seerr"
)

const updateInterval = 24 * time.Hour
const mappingURL = "https://raw.githubusercontent.com/Anime-Lists/anime-lists/master/anime-list.xml"

func fetchAndParseAnimeList(cacheDir string) ([]AnimeList.Anime, error) {
	var animeList AnimeList.AnimeList

	filename := filepath.Join(cacheDir, filepath.Base(mappingURL))

	if fi, statErr := os.Stat(filename); statErr == nil && time.Since(fi.ModTime()) < updateInterval {
		file, err := os.Open(filename)
		if err != nil {
			return nil, err
		}
		defer file.Close()

		if err := xml.NewDecoder(file).Decode(&animeList); err != nil {
			return nil, err
		}
	} else {
		req, err := http.NewRequest(http.MethodGet, mappingURL, nil)
		if err != nil {
			return nil, err
		}

		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("unexpected status: %s", resp.Status)
		}

		// https://github.com/natefinch/atomic/blob/master/atomic.go
		dir, file := filepath.Split(filename)
		if dir == "" {
			dir = "."
		}

		f, err := os.CreateTemp(dir, file)
		if err != nil {
			return nil, fmt.Errorf("cannot create temp file: %v", err)
		}
		defer func() {
			if err != nil {
				_ = os.Remove(f.Name())
			}
		}()
		defer f.Close()
		fname := f.Name()

		r := io.TeeReader(resp.Body, f)
		err = xml.NewDecoder(r).Decode(&animeList)
		if err != nil {
			return nil, err
		}

		err = f.Sync()
		if err != nil {
			return nil, fmt.Errorf("cannot flush tempfile %q: %v", fname, err)
		}
		err = f.Close()
		if err != nil {
			return nil, fmt.Errorf("cannot close tempfile %q: %v", fname, err)
		}

		if statErr == nil {
			if fileMode := fi.Mode(); fileMode != 0 {
				err = os.Chmod(fname, fileMode)
				if err != nil {
					return nil, fmt.Errorf("cannot set filemode on tempfile %q: %v", fname, err)
				}
			}
		}
		err = atomic.ReplaceFile(fname, filename)
		if err != nil {
			return nil, fmt.Errorf("cannot replace %q with tempfile %q: %v", filename, fname, err)
		}
	}

	return animeList.Anime, nil
}

func getAlreadyBlocklisted(seerrBlocklistClient *seerrApi.Client) (blocklisted map[int]struct{}, err error) {
	const take = math.MaxInt16 // 100
	skip := 0

	values := url.Values{
		"take": []string{strconv.Itoa(take)},
		//"search": []string{""},
		"filter": []string{seerrApi.GetBlocklistParamsFilterAll},
	}

	for {
		var resp seerrApi.GetBlocklistResponse
		values.Set("skip", strconv.Itoa(skip))

		err = seerrBlocklistClient.Get("", values, &resp)
		if err != nil {
			return
		}

		pageInfo := resp.PageInfo
		if skip == 0 && blocklisted == nil {
			blocklisted = make(map[int]struct{}, pageInfo.Results)
		}

		for _, result := range resp.Results {
			if result.MediaType == seerrApi.MediaTypeTv {
				blocklisted[result.TmdbId] = struct{}{}
			}
		}

		if pageInfo.Page >= pageInfo.Pages {
			break
		}

		if len(resp.Results) == 0 {
			break
		}

		skip += take
	}

	return
}

func main() {
	var cacheDir string
	var verbose bool

	exe, err := os.Executable()
	if err != nil {
		log.Fatal(err)
	}
	exeDir := filepath.Dir(exe)

	flag.StringVar(&cacheDir, "cache-dir", exeDir, "Folder to store downloaded files in")
	flag.BoolVar(&verbose, "verbose", false, "Verbose output")
	flag.Parse()

	for _, f := range []string{".env", filepath.Join(exeDir, ".env")} {
		if err := godotenv.Load(f); err != nil && !errors.Is(err, fs.ErrNotExist) {
			log.Fatalf("%s: %v", f, err)
		}
	}
	seerrHost := os.Getenv("SEERR_HOST")
	seerrApiKey := os.Getenv("SEERR_API_KEY")
	seerrUserId, err := strconv.Atoi(os.Getenv("SEERR_USER_ID"))
	if seerrHost == "" || seerrApiKey == "" || err != nil {
		log.Fatal("$SEERR_HOST/$SEERR_API_KEY/$SEERR_USER_ID are required")
	}

	seerrBlocklistClient, err := seerrApi.NewClient(seerrHost, seerrApiKey, "blocklist")
	if err != nil {
		log.Fatal(err)
	}

	blocklisted, err := getAlreadyBlocklisted(seerrBlocklistClient)
	if err != nil {
		log.Fatal(err)
	}

	fdp, err := fetchAndParseAnimeList(cacheDir)
	if err != nil {
		log.Fatal(err)
	}

	blocklistReqBody := &seerrApi.PostBlocklistJSONRequestBody{
		MediaType: seerrApi.MediaTypeTv,
		User:      seerrUserId,
	}

	for _, p := range fdp {
		tmdbId := p.Tmdbtv
		if tmdbId == 0 {
			continue
		}

		if _, ok := blocklisted[tmdbId]; !ok {
			if verbose {
				fmt.Printf("Adding %s (%v)\n", p.Name, tmdbId)
			}
			blocklistReqBody.TmdbId = tmdbId
			blocklistReqBody.Title = p.Name
		retry:
			err = seerrBlocklistClient.Post("", nil, blocklistReqBody, nil)
			if err != nil {
				_, ok = blocklisted[tmdbId]
				if httpErr, ok2 := errors.AsType[*seerrApi.HTTPError](err); !ok && ok2 && httpErr.StatusCode == http.StatusPreconditionFailed {
					// On TMDB, IDs can be shared between shows and movies; Seerr doesn't differentiate, so delete the
					// existing movie and attempt to re-add the anime series
					blocklisted[tmdbId] = struct{}{}
					if seerrBlocklistClient.Delete(fmt.Sprintf("/%d", tmdbId), nil, nil) == nil {
						goto retry
					}
					continue
				}
				log.Printf("Error adding %s (%v) to blocklist: %v", p.Name, tmdbId, err)
			} else {
				blocklisted[tmdbId] = struct{}{}
			}
		}
	}
}
