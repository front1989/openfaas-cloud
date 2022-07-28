edge-auth
=======

The edge-auth service can be used to evaluate whether access is available to a given resource

For example:

Check access to resource (r) /function/system-dashboard:

```
http://edge-auth:8080/q/?r=/function/system-dashboard
```

Responses:

* 200 - OK
* 301 - Cookie not present, redirect to given URL to create a valid cookie/login
* 401 - Cookie present, but invalid

Cookies:

* `openfaas_cloud`

This cookie is issued as part of the social sign-in flow using GitHub.

Contents (encoded JWT):

```json
{
  "name": "Alex Ellis",
  "access_token": "token-value",
  "organizations": "som-org",
  "aud": ".system.gw.io",
  "exp": 1537957152,
  "jti": "integer-value-here",
  "iat": 1537784352,
  "iss": "openfaas-cloud@github",
  "sub": "alexellis"
}
```

Please note - You need to be a public member of any Organisation that you wish to be able to see the dashboard and functions for.

## Building

```
export TAG=0.8.0
make build push
```

## Running

All environmental variables must be set and configured for the service whether running locally as a container, via Swarm or on Kubernetes.

* `/system-dashboard` is protected by OAuth
* All pipeline functions in OpenFaaS Cloud's stack.yml are blocked by default from all ingress such as `git-tar` and `buildshiprun`

### Generate a key/pair

This key/pair is used to sign the JWT and then verify it later.

```
# Private key
openssl ecparam -genkey -name prime256v1 -noout -out key

# Public key
openssl ec -in key -pubout -out key.pub
```

For Kubernetes store these secrets:

```sh
kubectl -n openfaas create secret generic jwt-private-key --from-file=./key
kubectl -n openfaas create secret generic jwt-public-key --from-file=./key.pub
```

For Swarm you can create these secrets:

```sh
docker secret create jwt-private-key ./key
docker secret create jwt-public-key ./key.pub
```

### Store your `client_secret` in a secret


```sh
export CLIENT_SECRET=""
```

For Kubernetes store these secrets:


```sh
kubectl -n openfaas create secret generic of-client-secret --from-literal="of-client-secret=$CLIENT_SECRET"
```

For Swarm you can create these secrets:

```sh
echo -n "$CLIENT_SECRET" | docker secret create of-client-secret -
```

### As a local container:

```sh
docker rm -f edge-auth
export TAG=0.8.0

docker run \
 -e client_secret="$CLIENT_SECRET" \
 -e client_id="$CLIENT_ID" \
 -e PORT=8080 \
 -p 8880:8080 \
 -e external_redirect_domain="http://auth.system.gw.io/" \
 -e cookie_root_domain=".system.gw.io" \
 -e public_key_path=/tmp/key.pub \
 -e private_key_path=/tmp/key \
 -e oauth_provider="github" \
 -v "`pwd`/key:/tmp/key" \
 -v "`pwd`/key.pub:/tmp/key.pub" \
 --name edge-auth -ti openfaas/edge-auth:${TAG}
```

### On Kubernetes

Edit `yaml/core/edge-auth-dep.yml` as needed and apply that file.

### GitLab integration

If you want to integrate OpenFaaS Cloud with your self-managed GitLab you need to set env variables, where instead of ... you should put valid url to your self-hosted GitLab (for example: https://gitlab.domain.com):

```
oauth_provider="gitlab"
oauth_provider_base_url="https://gitlab.domain.com"
```
