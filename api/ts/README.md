# @nearhost/api

TypeScript types for the Nearhost core's control API, generated from the
OpenAPI 3.1 schema in `openapi.yaml` with
[openapi-typescript](https://openapi-ts.dev). The package holds types only, with
no runtime code.

## Install

```sh
npm install --save-dev @nearhost/api
```

## Use

```ts
import type { paths, components } from '@nearhost/api';

type State = components['schemas']['State'];
type Envelope = components['schemas']['Envelope'];

// The JSON body of a successful GET /v1/state.
type StateResponse =
  paths['/v1/state']['get']['responses']['200']['content']['application/json'];
```

Lists and maps that the core can leave nil arrive as `null`, and the types say
so (`Item[] | null`). Handle both.

The schema file ships too: `@nearhost/api/openapi.yaml`. A running core serves
the same document at `GET /v1/openapi.yaml`.

## Versions

The package version is the Nearhost core release version. The control API has
its own version, in `info.version` of the schema. Install the `@nearhost/api`
version that matches the core you talk to.

## Maintaining

`index.d.ts` is generated and checked in. After changing `api/openapi.yaml`, run
`make api-types` from the repository root and commit the result. CI fails when
the committed file is out of date.

## Licence

MIT
