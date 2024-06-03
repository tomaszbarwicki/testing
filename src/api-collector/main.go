/*******************************************************************************
 * Copyright (c) 2024 Contributors to the Eclipse Foundation
 *
 * See the NOTICE file(s) distributed with this work for additional
 * information regarding copyright ownership.
 *
 * This program and the accompanying materials are made available under the
 * terms of the Apache License, Version 2.0 which is available at
 * https://www.apache.org/licenses/LICENSE-2.0.
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS, WITHOUT
 * WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the
 * License for the specific language governing permissions and limitations
 * under the License.
 *
 * SPDX-License-Identifier: Apache-2.0
 ******************************************************************************/

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"strings"

	"github.com/google/go-github/v61/github"
	"golang.org/x/oauth2"
	"gopkg.in/yaml.v2"
)

const API_DOCS_REPO = "api-hub"

type assetInfo struct {
	id   int64
	name string
}

type OpenAPIInfo struct {
	Version string `yaml:"version"`
	Title   string `yaml:"title"`
}
type OpenAPISpec struct {
	OpenAPI string      `yaml:"openapi"`
	Info    OpenAPIInfo `yaml:"info"`
}

func main() {
	owner, token := getArgs()
	ctx := context.Background()
	client := getAuthenticatedClient(ctx, token)
	repos, err := getOrgRepos(ctx, owner, client)
	if err != nil {
		log.Fatalf("Error fetching repos: %s", err)
	}
	for _, repo := range repos {
		log.Println("Scanning repo ", *repo.Name)
		specAssets := getAPISpecAssets(ctx, client, owner, *repo.Name)
		if specAssets == nil {
			log.Println("\t- No OpenAPI specs found")
			continue
		}
		downloadedSpecs := downloadAPISpecs(ctx, client, owner, *repo.Name, specAssets)
		commitDownloadedSpec(ctx, client, owner, API_DOCS_REPO, downloadedSpecs)
	}
}

func getArgs() (string, string) {
	owner := flag.String("owner", "", "Specify GitHub User or Organization")
	token := flag.String("token", "", "Specify GitHub Token")
	flag.Parse()

	if *owner == "" || *token == "" {
		log.Fatalln("Missing required arguments, please specify -owner [owner] and -token [token]")
	}
	return *owner, *token
}

func getAuthenticatedClient(ctx context.Context, gitToken string) *github.Client {
	ts := oauth2.StaticTokenSource(
		&oauth2.Token{AccessToken: gitToken},
	)
	tc := oauth2.NewClient(ctx, ts)

	return github.NewClient(tc)
}

func getOrgRepos(ctx context.Context, gitOwner string, client *github.Client) ([]*github.Repository, error) {
	opt := &github.RepositoryListByOrgOptions{
		ListOptions: github.ListOptions{},
	}

	var allRepos []*github.Repository

	for {
		repos, response, err := client.Repositories.ListByOrg(ctx, gitOwner, opt)
		if err != nil {
			return nil, err
		}
		allRepos = append(allRepos, repos...)
		if response.NextPage == 0 {
			break
		}
		opt.Page = response.NextPage
	}

	return allRepos, nil
}

func getAPISpecAssets(ctx context.Context, client *github.Client, owner string, repo string) []assetInfo {
	var apiSpecs []assetInfo
	release, _, err := client.Repositories.GetLatestRelease(ctx, owner, repo)
	if err != nil {
		log.Println("\t- No release found")
		return apiSpecs
	}
	log.Printf("\t+ Latest release found: %s\n", *release.Name)
	assets, _, err := client.Repositories.ListReleaseAssets(ctx, owner, repo, *release.ID, nil)
	if err != nil {
		log.Println("\t- No assets found in the release")
		return apiSpecs
	}
	for _, asset := range assets {
		if strings.Contains(*asset.Name, "_openapi.yaml") || strings.Contains(*asset.Name, "_openapi.yml") {
			apiSpecs = append(apiSpecs, assetInfo{*asset.ID, *asset.Name})
		}
	}
	return apiSpecs
}

func downloadAPISpecs(ctx context.Context, client *github.Client, owner string, repo string, assets []assetInfo) []string {
	var downloadedSpecs []string
	for _, asset := range assets {
		assetReader, assetURL, err := client.Repositories.DownloadReleaseAsset(ctx, owner, repo, asset.id, nil)
		if err != nil {
			log.Printf("\t- Error downloading OpenAPI spec %s: %s\n", asset.name, err)
			continue
		}
		var reader io.ReadCloser
		if assetReader == nil {
			resp, err := http.Get(assetURL)
			if err != nil {
				log.Printf("\t- Error downloading OpenAPI spec %s: %s\n", asset.name, err)
				continue
			}
			reader = resp.Body
			defer resp.Body.Close()
		} else {
			reader = assetReader
			defer assetReader.Close()
		}
		content, err := io.ReadAll(reader)
		if err != nil {
			log.Printf("\t- Error reading OpenAPI spec %s: %s\n", asset.name, err)
			continue
		}
		var spec OpenAPISpec
		err = yaml.Unmarshal(content, &spec)
		if err != nil {
			log.Printf("\t- Error parsing OpenAPI spec yaml format: %s\n", err)
			continue
		}
		dirPath := path.Join("docs", repo, spec.Info.Version)
		err = os.MkdirAll(dirPath, os.ModePerm)
		if err != nil {
			log.Printf("\t- Error creating directory: %s\n", err)
			continue
		}
		filePath := path.Join(dirPath, asset.name)
		err = os.WriteFile(filePath, content, 0644)
		if err != nil {
			log.Printf("\t- Error saving OpenAPI spec content to file: %s\n", err)
			continue
		}
		downloadedSpecs = append(downloadedSpecs, filePath)
		log.Printf("\t+ OpenAPI spec %s downloaded successfully\n", asset.name)

	}
	return downloadedSpecs
}

func commitDownloadedSpec(ctx context.Context, client *github.Client, owner string, repo string, specs []string) {
	for _, spec := range specs {
		content, err := os.ReadFile(spec)
		if err != nil {
			log.Printf("\t- Error reading specification file: %v\n", err)
			continue
		}
		fileOpt := &github.RepositoryContentGetOptions{
			Ref: "main",
		}
		_, _, _, err = client.Repositories.GetContents(ctx, owner, repo, spec, fileOpt)
		if err != nil && content != nil {
			if githubErr, ok := err.(*github.ErrorResponse); ok && githubErr.Response.StatusCode == 404 {
				fileOpt := &github.RepositoryContentFileOptions{
					Message: github.String(fmt.Sprintf("chore: upload OpenAPI spec: %s", spec)),
					Content: content,
				}
				_, _, err := client.Repositories.CreateFile(ctx, owner, repo, spec, fileOpt)
				if err != nil {
					log.Printf("\t- Error uploading file: %s\n", err)
					continue
				}
				log.Printf("\t+ Uploaded successfully %s\n", spec)
			}
		} else {
			log.Printf("\t- Spec file %s already exists, skipping upload.\n", spec)
		}
	}
}
