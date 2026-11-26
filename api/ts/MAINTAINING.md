# Maintaining @nearhost/api

`index.d.ts` is generated and checked in. After changing `api/openapi.yaml`, run
`make api-types` from the repository root and commit the result. CI fails when
the committed file is out of date.

`npm/build.mjs` copies only `index.d.ts` and `README.md` into the package, so
this file stays in the repository. Keep `README.md` for the package's users.
