/*
 * Copyright 2017-2020 Dgraph Labs, Inc. and Contributors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package edgraph

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/dgraph-io/dgraph/x"

	"github.com/dgraph-io/dgo/v200/protos/api"
	"github.com/dgraph-io/dgraph/graphql/schema"
	"github.com/golang/glog"
	"github.com/pkg/errors"
)

// ProcessPersistedQuery stores and retrieves persisted queries by following waterfall logic:
// 1. If sha256Hash is not provided process queries without persisting
// 2. If sha256Hash is provided try retrieving persisted queries
//		2a. Persisted Query not found
//		    i) If query is not provided then throw "PersistedQueryNotFound"
//			ii) If query is provided then store query in dgraph only if sha256 of the query is correct
//				otherwise throw "provided sha does not match query"
//      2b. Persisted Query found
//		    i)  If query is not provided then update gqlRes with the found query and proceed
//			ii) If query is provided then match query retrieved, if identical do nothing else
//				throw "query does not match persisted query"
func ProcessPersistedQuery(ctx context.Context, gqlReq *schema.Request) error {
	query := gqlReq.Query
	sha256Hash := gqlReq.Extensions.PersistedQuery.Sha256Hash

	if sha256Hash == "" {
		return nil
	}

	queryForSHA := `query Me($sha: string){
						me(func: eq(dgraph.graphql.p_sha256hash, $sha)){
							dgraph.graphql.p_query
						}
					}`
	variables := map[string]string{
		"$sha": sha256Hash,
	}
	req := &Request{
		req: &api.Request{
			Query:    queryForSHA,
			Vars:     variables,
			ReadOnly: true,
		},
		doAuth: NoAuthorize,
	}
	ctx = x.AttachNamespace(ctx, x.GalaxyNamespace)
	storedQuery, err := (&Server{}).doQuery(ctx, req)

	if err != nil {
		glog.Errorf("Error while querying sha %s", sha256Hash)
		return err
	}

	type shaQueryResponse struct {
		Me []struct {
			PersistedQuery string `json:"dgraph.graphql.p_query"`
		} `json:"me"`
	}

	shaQueryRes := &shaQueryResponse{}
	if len(storedQuery.Json) > 0 {
		if err := json.Unmarshal(storedQuery.Json, shaQueryRes); err != nil {
			return err
		}
	}

	if len(shaQueryRes.Me) == 0 {
		if query == "" {
			return errors.New("PersistedQueryNotFound")
		}
		if match, err := hashMatches(query, sha256Hash); err != nil {
			return err
		} else if !match {
			return errors.New("provided sha does not match query")
		}

		req = &Request{
			req: &api.Request{
				Mutations: []*api.Mutation{
					{
						Set: []*api.NQuad{
							{
								Subject:     "_:a",
								Predicate:   "dgraph.graphql.p_query",
								ObjectValue: &api.Value{Val: &api.Value_StrVal{StrVal: query}},
							},
							{
								Subject:     "_:a",
								Predicate:   "dgraph.graphql.p_sha256hash",
								ObjectValue: &api.Value{Val: &api.Value_StrVal{StrVal: sha256Hash}},
							},
							{
								Subject:   "_:a",
								Predicate: "dgraph.type",
								ObjectValue: &api.Value{Val: &api.Value_StrVal{
									StrVal: "dgraph.graphql.persisted_query"}},
							},
						},
					},
				},
				CommitNow: true,
			},
			doAuth: NoAuthorize,
		}

		ctx := context.WithValue(ctx, IsGraphql, true)
		ctx = x.AttachNamespace(ctx, x.GalaxyNamespace)
		_, err := (&Server{}).doQuery(ctx, req)
		return err

	}

	if len(shaQueryRes.Me) != 1 {
		return fmt.Errorf("same sha returned %d queries", len(shaQueryRes.Me))
	}

	if len(query) > 0 && shaQueryRes.Me[0].PersistedQuery != query {
		return errors.New("query does not match persisted query")
	}

	gqlReq.Query = shaQueryRes.Me[0].PersistedQuery
	return nil

}

func hashMatches(query, sha256Hash string) (bool, error) {
	hasher := sha256.New()
	_, err := hasher.Write([]byte(query))
	if err != nil {
		return false, err
	}
	hashGenerated := hex.EncodeToString(hasher.Sum(nil))
	return hashGenerated == sha256Hash, nil
}
