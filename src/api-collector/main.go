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

type OpenAPIInfo struct {
	Version string `yaml:"version"`
	Title   string `yaml:"title"`
}
type OpenAPISpec struct {
	OpenAPI string      `yaml:"openapi"`
	Info    OpenAPIInfo `yaml:"info"`
}

const MetadataFilename = ".tractusx"

type Metadata struct {
	ProductName  string   `yaml:"product"`
	OpenApiSpecs []string `yaml:"openApiSpecs"`
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
		specsUrls, err := getAPISpecsUrls(ctx, client, owner, *repo.Name)
		if err != nil {
			log.Println(err)
			continue
		}
		if specsUrls == nil {
			log.Println("No OpenAPI specs found")
			continue
		}

		downloadedSpecs := downloadAPISpecs(*repo.Name, specsUrls)
		for downloadedSpec := range downloadedSpecs {
			log.Println(downloadedSpec)
		}
		// commitDownloadedSpec(ctx, client, owner, API_DOCS_REPO, downloadedSpecs)
	}
}

func MetadataFromFile(fileContent []byte) (*Metadata, error) {
	var metadata Metadata

	err := yaml.Unmarshal(fileContent, &metadata)
	if err != nil {
		fmt.Println("Error parsing product metadata file")
		return nil, err
	}

	return &metadata, nil
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

func getAPISpecsUrls(ctx context.Context, client *github.Client, owner string, repo string) ([]string, error) {
	metadataFile, _, _, err := client.Repositories.GetContents(ctx, owner, repo, MetadataFilename, &github.RepositoryContentGetOptions{
		Ref: "main",
	})
	if err != nil {
		return nil, fmt.Errorf("error getting .tractusx metadata file: %v", err)
	}
	content, err := metadataFile.GetContent()
	if err != nil {
		return nil, fmt.Errorf("error getting metadata content: %v", err)
	}
	m, err := MetadataFromFile([]byte(content))
	if err != nil {
		return nil, fmt.Errorf("error parsing metadata: %v", err)
	}
	return m.OpenApiSpecs, nil
}

func downloadAPISpecs(repo string, specsUrls []string) []string {
	var downloadedSpecs []string
	for _, url := range specsUrls {
		resp, err := http.Get(url)
		if err != nil {
			log.Printf("Error downloading API spec file: %v\n", err)
			continue
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			log.Printf("Bad HTTP status: %s\n", resp.Status)
			continue
		}
		content, err := io.ReadAll(resp.Body)
		if err != nil {
			log.Printf("Error reading http response: %v\n", err)
			continue
		}
		var spec OpenAPISpec
		err = yaml.Unmarshal(content, &spec)
		if err != nil {
			log.Printf("Error parsing OpenAPI spec yaml format: %s\n", err)
			continue
		}
		dirPath := path.Join("docs", repo, spec.Info.Version)
		err = os.MkdirAll(dirPath, os.ModePerm)
		if err != nil {
			log.Printf("Error creating directory: %s\n", err)
			continue
		}
		urlSplit := strings.Split(url, "/")
		specName := urlSplit[len(urlSplit)-1]
		filePath := path.Join(dirPath, specName)
		err = os.WriteFile(filePath, content, 0644)
		if err != nil {
			log.Printf("Error saving OpenAPI spec content to file: %s\n", err)
			continue
		}
		downloadedSpecs = append(downloadedSpecs, filePath)
		log.Printf("OpenAPI spec %s downloaded successfully\n", specName)
	}
	return downloadedSpecs
}

func commitDownloadedSpec(ctx context.Context, client *github.Client, owner string, repo string, specs []string) {
	for _, spec := range specs {
		content, err := os.ReadFile(spec)
		if err != nil {
			log.Printf("Error reading specification file: %v\n", err)
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
					log.Printf("Error uploading file: %s\n", err)
					continue
				}
				log.Printf("Uploaded successfully %s\n", spec)
			}
		} else {
			log.Printf("Spec file %s already exists, skipping upload.\n", spec)
		}
	}
}
