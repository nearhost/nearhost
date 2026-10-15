# Changelog

## [0.2.0](https://github.com/nearhost/nearhost/compare/v0.1.0...v0.2.0) (2026-10-15)


### Features

* add projects from the interface ([#68](https://github.com/nearhost/nearhost/issues/68)) ([eb41706](https://github.com/nearhost/nearhost/commit/eb41706beaaf551334341ab956f6a955727481bf))
* add servers from the interface ([#67](https://github.com/nearhost/nearhost/issues/67)) ([d65de8e](https://github.com/nearhost/nearhost/commit/d65de8ed9ef69d0844e2f551568d095edadf12b0))
* add the full-screen interface ([#55](https://github.com/nearhost/nearhost/issues/55)) ([075efe6](https://github.com/nearhost/nearhost/commit/075efe625b9ab7cfd9efd0759bb32a5cb03fb8c9))
* change route roles and read advice in the project panel ([#76](https://github.com/nearhost/nearhost/issues/76)) ([f147731](https://github.com/nearhost/nearhost/commit/f14773105def8f9ab9febb1acce22d78c3532aac))
* edit and remove projects from the interface ([#77](https://github.com/nearhost/nearhost/issues/77)) ([cbd426a](https://github.com/nearhost/nearhost/commit/cbd426aa641ed1cbc27b3203e2e1bbe674bf853c))
* give the interface what adding a project or server needs ([#66](https://github.com/nearhost/nearhost/issues/66)) ([4f6a9ea](https://github.com/nearhost/nearhost/commit/4f6a9ea43c6edde88eebc7eda02fdad84d1e2419))
* let the engine take a changed config while it runs ([#53](https://github.com/nearhost/nearhost/issues/53)) ([aa1013a](https://github.com/nearhost/nearhost/commit/aa1013aff4ca848c38e01d5b424fefd54ed47bf7))
* let the interface edit the config, reconnect servers, read advice and trust the CA ([#75](https://github.com/nearhost/nearhost/issues/75)) ([9d11b14](https://github.com/nearhost/nearhost/commit/9d11b14d04fa165aeae5a1c086e1248564f03fd2))
* open the full-screen interface from bare nearhost ([#56](https://github.com/nearhost/nearhost/issues/56)) ([4b5bbf9](https://github.com/nearhost/nearhost/commit/4b5bbf9c981987f7a34a641f9ead7602e3bd8146))
* reconnect, edit and remove servers from the interface ([#78](https://github.com/nearhost/nearhost/issues/78)) ([f7fe012](https://github.com/nearhost/nearhost/commit/f7fe012dcc93b6f8bd20b81e36cf65ae29b8b954))
* walk a first run through trusting the local CA ([#79](https://github.com/nearhost/nearhost/issues/79)) ([331d426](https://github.com/nearhost/nearhost/commit/331d42688e54f9bbe2a25eb05d619e2680d856e6))


### Bug Fixes

* keep a failed add's error last, run one apply at a time, say how to retry, end an apply on quit ([#61](https://github.com/nearhost/nearhost/issues/61)) ([7c862f3](https://github.com/nearhost/nearhost/commit/7c862f393c85675df6c06f8a81040b38f0190c38))
* keep form answers to their form, close every connection, take pastes, reconnect with ctrl+r ([#69](https://github.com/nearhost/nearhost/issues/69)) ([6d1f008](https://github.com/nearhost/nearhost/commit/6d1f008e36782770389dfff75bedbbac1e6245e8))
* keep the not-found mark off the config, guard reconnects and role edits, keep the edited path, retry advice ([#80](https://github.com/nearhost/nearhost/issues/80)) ([2d1fda0](https://github.com/nearhost/nearhost/commit/2d1fda092188756b2c681681af6c44d30a469f3e))
* let ctrl+c end the add questions and keep stale password prompts off the screen ([#62](https://github.com/nearhost/nearhost/issues/62)) ([ea3266d](https://github.com/nearhost/nearhost/commit/ea3266da5f7349eb6d1d1b4e8de9507fd452467a))
* let saves finish, keep the alias with the name, advise for the final choices ([#72](https://github.com/nearhost/nearhost/issues/72)) ([1857a98](https://github.com/nearhost/nearhost/commit/1857a98295ce0c9b7e43123beaca6ff133e9bcf1))
* read ssh hosts in the background, one host rule, and the spec's wording in the forms ([#73](https://github.com/nearhost/nearhost/issues/73)) ([ba1c2a8](https://github.com/nearhost/nearhost/commit/ba1c2a8e00e36e54f178ad6d3d09420431bf8d48))
* refuse a start on a project apply replaced, even when it is busy ([#60](https://github.com/nearhost/nearhost/issues/60)) ([dfd0409](https://github.com/nearhost/nearhost/commit/dfd040929976d205162baf56be11b2b7db2937c8))
* scroll the sidebar to the selection, open a new project's server, drop format characters ([#63](https://github.com/nearhost/nearhost/issues/63)) ([b649442](https://github.com/nearhost/nearhost/commit/b6494424156d4e9b6e2dbb2043e3355f86689d0f))
* settle config saves, show refused starts and the port hint, read the machine on the clock ([#57](https://github.com/nearhost/nearhost/issues/57)) ([d55eb0f](https://github.com/nearhost/nearhost/commit/d55eb0f3eb641caa2088d7e4968cabf666a88a17))
* stop a status test failing on events published after it reads ([#59](https://github.com/nearhost/nearhost/issues/59)) ([2984bc1](https://github.com/nearhost/nearhost/commit/2984bc1b1b95747b7b8e161134f0a814e6d3d617))
* tidy server paths and complete .., kept prefixes and accented names ([#71](https://github.com/nearhost/nearhost/issues/71)) ([76c444b](https://github.com/nearhost/nearhost/commit/76c444bfd3fe9e9cf552ab19ad48cf99df285bc7))
* wait for the server check to begin before quitting in its test, and gofmt a test ([#74](https://github.com/nearhost/nearhost/issues/74)) ([9d232f0](https://github.com/nearhost/nearhost/commit/9d232f08b4bdbfb5e99c12fed288508fe0d277b3))

## 0.1.0 (2026-10-14)


### Features

* add config model with load, save and validation ([#2](https://github.com/nearhost/nearhost/issues/2)) ([33f2bfa](https://github.com/nearhost/nearhost/commit/33f2bfab827f65ae8cae96523fbce33985fcae3c))
* add discovery file reader and env parsing ([#4](https://github.com/nearhost/nearhost/issues/4)) ([7d74086](https://github.com/nearhost/nearhost/commit/7d74086b0c936903308d2961dcd0cd04118e5c9c))
* add engine orchestrating projects end to end ([#15](https://github.com/nearhost/nearhost/issues/15)) ([81e604f](https://github.com/nearhost/nearhost/commit/81e604fe8e51f49d49b7a2a1d22dbc830f17d4f9))
* add local ca trust commands ([#13](https://github.com/nearhost/nearhost/issues/13)) ([74b4464](https://github.com/nearhost/nearhost/commit/74b446432e6ce89fe395feaa14d6ad5117ecc4da))
* add preset index to build the registry index ([#30](https://github.com/nearhost/nearhost/issues/30)) ([81e8ab5](https://github.com/nearhost/nearhost/commit/81e8ab5dd373446704b1e859a09482c2ebe420ec))
* add preset list, enable, disable and validate ([#25](https://github.com/nearhost/nearhost/issues/25)) ([9a1dc2a](https://github.com/nearhost/nearhost/commit/9a1dc2af6a4c1a45d884db891b7c14018011e231))
* add preset registry commands ([#36](https://github.com/nearhost/nearhost/issues/36)) ([091dd12](https://github.com/nearhost/nearhost/commit/091dd12572f539fed094c37cddfb70910381992d))
* add preset test to run fixtures ([#26](https://github.com/nearhost/nearhost/issues/26)) ([2901c36](https://github.com/nearhost/nearhost/commit/2901c366cd32d142a4973ea00573aadec16b9b12))
* add registry client with cached index and hash-checked downloads ([#27](https://github.com/nearhost/nearhost/issues/27)) ([ec442f1](https://github.com/nearhost/nearhost/commit/ec442f168834192d963faa5167fddfa38c988d35))
* add ssh port forwards and sftp file reads ([#9](https://github.com/nearhost/nearhost/issues/9)) ([aeb30e5](https://github.com/nearhost/nearhost/commit/aeb30e5f346db9e35e911214d48e14a88894ed6a))
* add trusted preset registries to the config ([#32](https://github.com/nearhost/nearhost/issues/32)) ([2f2d6ee](https://github.com/nearhost/nearhost/commit/2f2d6ee124ad3ccd8e1d4e046280c5e4452d3317))
* add up, add, ls and doctor commands ([#16](https://github.com/nearhost/nearhost/issues/16)) ([ee85e13](https://github.com/nearhost/nearhost/commit/ee85e134b67023442c26a5f779272a3ae2346c8c))
* apply the presets enabled in the config when adding a project ([#23](https://github.com/nearhost/nearhost/issues/23)) ([702fee0](https://github.com/nearhost/nearhost/commit/702fee0fe4540f2b7638cdd74152e00a615aad79))
* ask in add whether to trust the official registry ([#43](https://github.com/nearhost/nearhost/issues/43)) ([5717f80](https://github.com/nearhost/nearhost/commit/5717f8067055112274850c90091d37448affc33f))
* ask which route is the web app when nothing says ([#42](https://github.com/nearhost/nearhost/issues/42)) ([34ef848](https://github.com/nearhost/nearhost/commit/34ef848b2f2351a890082136f3172c7d3e7e55f2))
* build caddy config for aliased sites ([#11](https://github.com/nearhost/nearhost/issues/11)) ([2d5bec6](https://github.com/nearhost/nearhost/commit/2d5bec6ec7dcf8e6cfdf59be852f4123388fc311))
* check and evaluate preset expressions with cel ([#19](https://github.com/nearhost/nearhost/issues/19)) ([1403449](https://github.com/nearhost/nearhost/commit/14034494c6be85086f5d727269d739cc8b5b621d))
* discover project routes and advisories ([#6](https://github.com/nearhost/nearhost/issues/6)) ([4462f3b](https://github.com/nearhost/nearhost/commit/4462f3baa4466e45da10b06a89da3b7e353a3475))
* evaluate presets against a project ([#20](https://github.com/nearhost/nearhost/issues/20)) ([d3c774f](https://github.com/nearhost/nearhost/commit/d3c774fbb9e407fab66a7f00233d570969fecaeb))
* install presets from each trusted registry ([#35](https://github.com/nearhost/nearhost/issues/35)) ([60162df](https://github.com/nearhost/nearhost/commit/60162df5339e518f13dd1773a24a24d08b02e8c8))
* install, list and update presets from the registry ([#28](https://github.com/nearhost/nearhost/issues/28)) ([b5d9a15](https://github.com/nearhost/nearhost/commit/b5d9a15d74ed0c5a86bdccaee22e6aa40c5919ce))
* list ssh config hosts ([#7](https://github.com/nearhost/nearhost/issues/7)) ([94cad92](https://github.com/nearhost/nearhost/commit/94cad927d86572a88220611366fb6530fd13d891))
* manage hosts file block with sudo apply ([#10](https://github.com/nearhost/nearhost/issues/10)) ([68043ab](https://github.com/nearhost/nearhost/commit/68043ab15771dc359d4b439060a9964dfbff41f7))
* offer to add an unknown server in add ([#50](https://github.com/nearhost/nearhost/issues/50)) ([9b5ac27](https://github.com/nearhost/nearhost/commit/9b5ac27ed941b51f9a1aaaae608794b75104e7f1))
* parse and validate preset files ([#18](https://github.com/nearhost/nearhost/issues/18)) ([1e83254](https://github.com/nearhost/nearhost/commit/1e83254cb58b2308c742ffddd2e1360cf3645ee4))
* parse compose published ports with variable resolution ([#5](https://github.com/nearhost/nearhost/issues/5)) ([c47b624](https://github.com/nearhost/nearhost/commit/c47b6244452054679a05b01a140b893e39fa744f))
* propose routes for the port variables in .env ([#39](https://github.com/nearhost/nearhost/issues/39)) ([e7f7296](https://github.com/nearhost/nearhost/commit/e7f729620978f3370d0bf9d13aaad19f14010c3e))
* read ~ in a server path as the home directory there ([#51](https://github.com/nearhost/nearhost/issues/51)) ([6463414](https://github.com/nearhost/nearhost/commit/6463414f12a503c1a15818c5461b0b1bb314e8ca))
* read compose ports only for a preset that asks for them ([#40](https://github.com/nearhost/nearhost/issues/40)) ([5c4afaf](https://github.com/nearhost/nearhost/commit/5c4afaff48de5c2aafcfefcdb3b0d9191da905a8))
* run embedded caddy proxy with log sink ([#12](https://github.com/nearhost/nearhost/issues/12)) ([4314e93](https://github.com/nearhost/nearhost/commit/4314e939c07c70ef806b4fc4df4630e8617e3994))
* suggest registry presets when adding a project ([#29](https://github.com/nearhost/nearhost/issues/29)) ([a427e7b](https://github.com/nearhost/nearhost/commit/a427e7b0194d359324cfaad8f1786b1cf99fa146))
* supervise openssh control master sessions ([#8](https://github.com/nearhost/nearhost/issues/8)) ([0222ebd](https://github.com/nearhost/nearhost/commit/0222ebd38eab3e2b7a33cf0e0899916eb2a0e85b))


### Bug Fixes

* close gaps found reviewing the presets stack ([#31](https://github.com/nearhost/nearhost/issues/31)) ([62e1506](https://github.com/nearhost/nearhost/commit/62e1506c022f6610647269e7c144ed59a93391e0))
* cover firefox trust store when system already trusts the ca ([#14](https://github.com/nearhost/nearhost/issues/14)) ([71a0e2f](https://github.com/nearhost/nearhost/commit/71a0e2f85f4c111ee3cfee4fb20f0b3b0edd12ff))
* tie a registry's cached index to its repo ([#37](https://github.com/nearhost/nearhost/issues/37)) ([ef2e885](https://github.com/nearhost/nearhost/commit/ef2e885fe68125a9572c4d977497b36e1c702b98))
* wait for every signal before a signal test stops listening ([#38](https://github.com/nearhost/nearhost/issues/38)) ([ff78e40](https://github.com/nearhost/nearhost/commit/ff78e40e5c0e01725acc50eaee6a163008bab717))
* word the add messages for registries and compose-only projects ([#44](https://github.com/nearhost/nearhost/issues/44)) ([87b7efe](https://github.com/nearhost/nearhost/commit/87b7efe16649c6dee0a959993f01c775c5bda7a7))
