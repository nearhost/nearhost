# Changelog

## [0.7.1](https://github.com/nearhost/nearhost/compare/v0.7.0...v0.7.1) (2026-10-20)


### Bug Fixes

* **proxy:** serve https://localhost while nearhost runs with no projects ([#203](https://github.com/nearhost/nearhost/issues/203)) ([2bd1771](https://github.com/nearhost/nearhost/commit/2bd177123ac447b54c7bcba4588c6dd588bbdf96))
* **tui:** keep the mark's shape when line height stretches cells ([#202](https://github.com/nearhost/nearhost/issues/202)) ([2bac077](https://github.com/nearhost/nearhost/commit/2bac077d0598accdf3617606332fa5611770a46e))

## [0.7.0](https://github.com/nearhost/nearhost/compare/v0.6.0...v0.7.0) (2026-10-20)


### Features

* **nearhostrc:** accept a file without routes, such as one that only sets the alias ([#198](https://github.com/nearhost/nearhost/issues/198)) ([200df61](https://github.com/nearhost/nearhost/commit/200df61c4ff67b07eb4a26c16aa24b9d165590b2))


### Bug Fixes

* **engine:** answer loopback forwards on [::1] too ([#197](https://github.com/nearhost/nearhost/issues/197)) ([b157466](https://github.com/nearhost/nearhost/commit/b157466361ffc2e712358e29b7dc71bb71666872))

## [0.6.0](https://github.com/nearhost/nearhost/compare/v0.5.0...v0.6.0) (2026-10-20)


### Features

* add /nearhost doctor to the skill, with doctor and reload in its script ([#185](https://github.com/nearhost/nearhost/issues/185)) ([0b43968](https://github.com/nearhost/nearhost/commit/0b43968efe944f416728cf8f36711298a7af5118))
* add host name patterns with wildcard labels and precedence ([#145](https://github.com/nearhost/nearhost/issues/145)) ([4a5a5b3](https://github.com/nearhost/nearhost/commit/4a5a5b37ed93b09c8f76fcf051776594f3f38225))
* add nearhost export, which writes a project's setup to its .nearhostrc ([#175](https://github.com/nearhost/nearhost/issues/175)) ([93cf29f](https://github.com/nearhost/nearhost/commit/93cf29f8033607c0c38bfb8983eefd27bb86e2e7))
* add nearhost update, and say when a newer release is out ([#143](https://github.com/nearhost/nearhost/issues/143)) ([ac7cffd](https://github.com/nearhost/nearhost/commit/ac7cffd7aed311ef2a6a10a555fe5df18178e2b3))
* add the doctor's findings model and its .nearhostrc, drift and advice checks ([#178](https://github.com/nearhost/nearhost/issues/178)) ([4a5b847](https://github.com/nearhost/nearhost/commit/4a5b847470f79064ab359bf22e100df0c3e831fc))
* ask before letting Docker containers reach a project, in nearhost add and the interface ([#166](https://github.com/nearhost/nearhost/issues/166)) ([14594df](https://github.com/nearhost/nearhost/commit/14594df81f91a3e0a1850a399522bf1bcd3754d8))
* check each enabled project in nearhost doctor, and print it all as JSON with --json ([#182](https://github.com/nearhost/nearhost/issues/182)) ([d30c497](https://github.com/nearhost/nearhost/commit/d30c4979114696f1d9698ffa8f88db35084c2d92))
* **cli:** serve inferred hosts on add, with .nearhostrc taking precedence ([#190](https://github.com/nearhost/nearhost/issues/190)) ([36a7fd8](https://github.com/nearhost/nearhost/commit/36a7fd883158fc8896716251f964bb8254983c12))
* **discovery:** infer route hosts from presets, and give advisories the project's hosts ([#189](https://github.com/nearhost/nearhost/issues/189)) ([ab0e2d0](https://github.com/nearhost/nearhost/commit/ab0e2d0918551cbc6f4eb8ce13baad17a56e05eb))
* **doctor:** report hosts the project's settings name that aren't served ([#192](https://github.com/nearhost/nearhost/issues/192)) ([f6b43f2](https://github.com/nearhost/nearhost/commit/f6b43f2cb99a9678522cde5cd783cc3e28f794c9))
* edit a route's hosts in the interface, show what .nearhostrc asks for, and approve it on save ([#162](https://github.com/nearhost/nearhost/issues/162)) ([a9bc91e](https://github.com/nearhost/nearhost/commit/a9bc91ea349b43d4063bc4856a9e64a6f43a88a1))
* give agents a project's findings with GET /v1/doctor, and list the API's capabilities ([#183](https://github.com/nearhost/nearhost/issues/183)) ([1908018](https://github.com/nearhost/nearhost/commit/1908018c8a58fa02921d418cb4240807ff69975c))
* issue certificates for wildcard hosts on demand, and redirect them to https with a 308 ([#149](https://github.com/nearhost/nearhost/issues/149)) ([0af6adf](https://github.com/nearhost/nearhost/commit/0af6adf3dffd4ab4e7b27fa5d62e4abd88b382af))
* let a .nearhostrc ask for docker_clients, keep it in the approved snapshot, and list it for approval ([#165](https://github.com/nearhost/nearhost/issues/165)) ([00519fb](https://github.com/nearhost/nearhost/commit/00519fb4df798f4f58e5b895dbaad4a1451e1618))
* let a .nearhostrc set the project's main name, with variables, applied like its hosts ([#172](https://github.com/nearhost/nearhost/issues/172)) ([3016644](https://github.com/nearhost/nearhost/commit/30166445f3ec491e1433eacd449d279af47a0ce7))
* let a project's main name be any domain, approved when it isn't .test ([#171](https://github.com/nearhost/nearhost/issues/171)) ([64c5fc0](https://github.com/nearhost/nearhost/commit/64c5fc04bf6b03eb8bdced2ba206a17ef85535b6))
* let a project's web routes list hosts, and allow more than one web route ([#146](https://github.com/nearhost/nearhost/issues/146)) ([69a5c6c](https://github.com/nearhost/nearhost/commit/69a5c6c8af9d4c912b8b8874abc1c2b02eedf6b2))
* let agents ask Nearhost to re-read a .nearhostrc, and wait for the user's approval ([#184](https://github.com/nearhost/nearhost/issues/184)) ([3930e13](https://github.com/nearhost/nearhost/commit/3930e13ec384c0c4f4d68084cafec3aa495e76c4))
* let nearhost add read .nearhostrc, take --host, and ask before serving hosts that need approval ([#160](https://github.com/nearhost/nearhost/issues/160)) ([f60f08f](https://github.com/nearhost/nearhost/commit/f60f08fc8fa782bae490834e77c30347ea16921a))
* merge a project's .nearhostrc with its routes, keeping hosts added by hand, and say which hosts need approval ([#158](https://github.com/nearhost/nearhost/issues/158)) ([d98fb77](https://github.com/nearhost/nearhost/commit/d98fb77029b6c35c9c7f9eb42ba5543c3eafe27f))
* **preset:** add preset schema 2, with hosts on routes and URL helpers ([#188](https://github.com/nearhost/nearhost/issues/188)) ([6cb6bc2](https://github.com/nearhost/nearhost/commit/6cb6bc21f3fb10554a63bae8dd2ea1148945bab8))
* put real-domain hosts in the hosts file only when DNS doesn't already send them to this machine ([#151](https://github.com/nearhost/nearhost/issues/151)) ([5b8a2b9](https://github.com/nearhost/nearhost/commit/5b8a2b974ce7892443d0e468b868687ae1a8e6bd))
* re-read .nearhostrc when a project starts, apply port-only changes, and hold the rest for approval ([#161](https://github.com/nearhost/nearhost/issues/161)) ([adadb3a](https://github.com/nearhost/nearhost/commit/adadb3a1eba960d7417fd6aa6a6312cbaf9ad214))
* read a project's .nearhostrc, with ports and hosts filled from .env.local and .env ([#157](https://github.com/nearhost/nearhost/issues/157)) ([4d838d9](https://github.com/nearhost/nearhost/commit/4d838d9e0cbae4c7b1afe74cced5184e91da583a))
* render a project as a .nearhostrc that reads back to the same project ([#173](https://github.com/nearhost/nearhost/issues/173)) ([b1ea464](https://github.com/nearhost/nearhost/commit/b1ea464784d4d0ab8ddc6ef926b29c787a872022))
* report containers that call a served host without host-gateway, docker_clients or the local CA ([#181](https://github.com/nearhost/nearhost/issues/181)) ([7aa6345](https://github.com/nearhost/nearhost/commit/7aa63450b84296f3da1a2cb37f79d742f954fd37))
* report hosts that don't reach Nearhost, overlapping patterns and web ports nothing listens on ([#180](https://github.com/nearhost/nearhost/issues/180)) ([84e7982](https://github.com/nearhost/nearhost/commit/84e7982bcc8ca4e5a085c57fcc816ff0719d6bf6))
* review and approve .nearhostrc changes in the interface, recheck a project on demand, and refuse clashing hosts when enabling ([#163](https://github.com/nearhost/nearhost/issues/163)) ([4ce25c9](https://github.com/nearhost/nearhost/commit/4ce25c9f977866b7df0ed8763215d8b184b35fb2))
* serve a site at several hosts, exact names before wildcards, with logs tagged by project ([#148](https://github.com/nearhost/nearhost/issues/148)) ([df60b38](https://github.com/nearhost/nearhost/commit/df60b3840ed1ec604b2918604d1bb57dd1ae5a96))
* serve a site to extra client ranges, matched by host at the handshake, so opted-in projects can be reached from containers ([#168](https://github.com/nearhost/nearhost/issues/168)) ([235b479](https://github.com/nearhost/nearhost/commit/235b47912d48647a07d6ff13c5da33efa1c395f4))
* serve projects that opt in to the local Docker networks, as the Docker daemon reports them, and follow its networks as they change ([#169](https://github.com/nearhost/nearhost/issues/169)) ([4f6a502](https://github.com/nearhost/nearhost/commit/4f6a5021bfe847eb569cd519e6b476a288f8ec04))
* show a Nearhost page at https://localhost when no project uses it ([#156](https://github.com/nearhost/nearhost/issues/156)) ([97107ac](https://github.com/nearhost/nearhost/commit/97107ac598634d1d5c603fb094ea3906082544ea))
* show every URL a project serves, to coding agents and in the interface ([#153](https://github.com/nearhost/nearhost/issues/153)) ([ea1076c](https://github.com/nearhost/nearhost/commit/ea1076c40c35ccaf8a5e7921a114e09d6a23a778))
* start every web route of a project, and refuse a host another project already serves ([#152](https://github.com/nearhost/nearhost/issues/152)) ([0d1dfb2](https://github.com/nearhost/nearhost/commit/0d1dfb230ff8f151783fa69625dacfda3ad41016))
* **tui:** add newly inferred hosts when a project is edited ([#191](https://github.com/nearhost/nearhost/issues/191)) ([53a5754](https://github.com/nearhost/nearhost/commit/53a575461ffab6f8788bf0ceda6b188488cce0d4))
* write project files atomically, locally and over SFTP ([#174](https://github.com/nearhost/nearhost/issues/174)) ([a6e8b99](https://github.com/nearhost/nearhost/commit/a6e8b99b440d6571701914a2d7409fcb8199de08))


### Bug Fixes

* address cubic's review of hosts from presets ([#196](https://github.com/nearhost/nearhost/issues/196)) ([3ef3a5f](https://github.com/nearhost/nearhost/commit/3ef3a5f39724518d13ecaf8de101baeb0572a906))
* address the final review of hosts from presets ([#193](https://github.com/nearhost/nearhost/issues/193)) ([be698a0](https://github.com/nearhost/nearhost/commit/be698a0de412a5586557d05b473d870fee764b46))
* address the final review of the project doctor ([#186](https://github.com/nearhost/nearhost/issues/186)) ([0920045](https://github.com/nearhost/nearhost/commit/092004556c254727f7301f1287ca2a851ee6310a))
* bind an exported main name only to a *_HOST variable, match the review on pending changes, and describe projects at their own domain ([#176](https://github.com/nearhost/nearhost/issues/176)) ([daece35](https://github.com/nearhost/nearhost/commit/daece354a3e04910baac66acdd06aad84c06f38f))
* close the multi-host follow-ups: hosts for hand-started projects, quiet reloads, and a port 80 that drops what it doesn't serve ([#155](https://github.com/nearhost/nearhost/issues/155)) ([0eb44ae](https://github.com/nearhost/nearhost/commit/0eb44aea52562773c5911a3ad99acf1d9553f7fe))
* keep hosts right on a --disabled add and when a route's role changes ([#195](https://github.com/nearhost/nearhost/issues/195)) ([8cde4c9](https://github.com/nearhost/nearhost/commit/8cde4c926ae5966cf5e01142e383f5e1eb9de38d))
* keep hosts through edits, cache DNS answers, and show each web route's port ([#154](https://github.com/nearhost/nearhost/issues/154)) ([175611a](https://github.com/nearhost/nearhost/commit/175611abb5306d4564c7e59d754b33e1cbf660f9))
* let the interface opt in to Docker clients, keep each name with its most specific host for Docker clients too, and word approval for options ([#170](https://github.com/nearhost/nearhost/issues/170)) ([e706f89](https://github.com/nearhost/nearhost/commit/e706f89dcafc4919b1e2c5b54232c92f6966ead6))
* refuse clashing hosts at start, share the interface's config lock, and keep hand-added hosts when a .nearhostrc port moves ([#164](https://github.com/nearhost/nearhost/issues/164)) ([b02b7ad](https://github.com/nearhost/nearhost/commit/b02b7adca7356cafee77a6d58a5421492b7468cd))
* tighten the project doctor's follow-ups from the final review ([#187](https://github.com/nearhost/nearhost/issues/187)) ([d3ea3be](https://github.com/nearhost/nearhost/commit/d3ea3bed5a0cecd29fddf0c217f1a010719bc5ef))

## [0.5.0](https://github.com/nearhost/nearhost/compare/v0.4.0...v0.5.0) (2026-10-16)


### Features

* add the nearhost plugin and skill for coding agents ([#131](https://github.com/nearhost/nearhost/issues/131)) ([412b69a](https://github.com/nearhost/nearhost/commit/412b69a8039aabc59d3e05ab44aa3e1331456ea8))
* check in doctor that coding agents can reach nearhost from each server ([#130](https://github.com/nearhost/nearhost/issues/130)) ([b57c24c](https://github.com/nearhost/nearhost/commit/b57c24c0c516d49a006d58ff15eb78c1c462bfa7))
* forward a socket for coding agents to each server ([#125](https://github.com/nearhost/nearhost/issues/125)) ([1ea1c76](https://github.com/nearhost/nearhost/commit/1ea1c76734104ed820dc3371b513e8eaaba41be9))
* let coding agents reach nearhost from this machine and its servers ([#127](https://github.com/nearhost/nearhost/issues/127)) ([beae57c](https://github.com/nearhost/nearhost/commit/beae57cb7e8beb293fadd504ff62abff5b12c898))
* notify the terminal when an agent proposes a project ([#129](https://github.com/nearhost/nearhost/issues/129)) ([58d582a](https://github.com/nearhost/nearhost/commit/58d582a9316cff6a6a9656f9b656e592b7ae0e55))
* open an agent's proposal in the add-project form ([#128](https://github.com/nearhost/nearhost/issues/128)) ([1bb16db](https://github.com/nearhost/nearhost/commit/1bb16db2b3f5482fef83a4b77baf1cb41821ec09))
* publish @nearhost/cli to npm, so npx @nearhost/cli runs nearhost ([#142](https://github.com/nearhost/nearhost/issues/142)) ([b5a5754](https://github.com/nearhost/nearhost/commit/b5a57544b63bde836a46508b7ae1b2a730b1e2af))
* serve an API for coding agents on Unix sockets ([#124](https://github.com/nearhost/nearhost/issues/124)) ([edcf1e9](https://github.com/nearhost/nearhost/commit/edcf1e98ee3e5309f4e2d245a61bd53c84057118))


### Bug Fixes

* find a project behind a symlink, and fail status for a path no project holds ([#134](https://github.com/nearhost/nearhost/issues/134)) ([a47edfd](https://github.com/nearhost/nearhost/commit/a47edfd1f572db1ada06f325a0e0abd5f6a5a615))
* give a declined proposal its last error, keep proposals in order and count only live ones ([#138](https://github.com/nearhost/nearhost/issues/138)) ([413b071](https://github.com/nearhost/nearhost/commit/413b0713ad557bcb193a496e650d8d94f06d203d))
* keep local agents local when a server is this machine, and say how to connect a server ([#132](https://github.com/nearhost/nearhost/issues/132)) ([43e1a00](https://github.com/nearhost/nearhost/commit/43e1a008bd2d02f8859dea6b0bc5f652e18b54e1))
* log a server's agent socket that cannot be served, and close it when the server goes ([#137](https://github.com/nearhost/nearhost/issues/137)) ([9b4f468](https://github.com/nearhost/nearhost/commit/9b4f4684de858f297bcc9afe678e34c9fefebf08))
* make a new install id when the config is copied to another machine ([#139](https://github.com/nearhost/nearhost/issues/139)) ([63af230](https://github.com/nearhost/nearhost/commit/63af230ef31bdccb55263d498953912cf5aa8bc8))
* say why agents cannot reach nearhost from a server, and what fixes it ([#135](https://github.com/nearhost/nearhost/issues/135)) ([115cb94](https://github.com/nearhost/nearhost/commit/115cb940dbd4a9eb5e4d9391e2bc2998ae940dc4))

## [0.4.0](https://github.com/nearhost/nearhost/compare/v0.3.0...v0.4.0) (2026-10-15)


### Features

* ask for the password in a box over the interface ([#112](https://github.com/nearhost/nearhost/issues/112)) ([26a1079](https://github.com/nearhost/nearhost/commit/26a1079ba4e4e18e649cf2aaa73d0d68f906df1b))
* check and cache sudo credentials, and run the hosts update without a prompt ([#111](https://github.com/nearhost/nearhost/issues/111)) ([45349d4](https://github.com/nearhost/nearhost/commit/45349d4fd7d6d28b0de8fea3a3c15f91a41538a2))
* make the terminal fallback Nearhost's own: a header, one prompt, and how it went ([#114](https://github.com/nearhost/nearhost/issues/114)) ([1341be4](https://github.com/nearhost/nearhost/commit/1341be45d271fa38bb3ae262c8b95ecfc213cede))
* run the hosts update and trust as root through the password box ([#113](https://github.com/nearhost/nearhost/issues/113)) ([4272e58](https://github.com/nearhost/nearhost/commit/4272e5806f124f614e472eb56db2fe2a2f990aac))
* suggest directories as the path is typed, on this machine and on servers ([#122](https://github.com/nearhost/nearhost/issues/122)) ([ce56f34](https://github.com/nearhost/nearhost/commit/ce56f3401b49fbc454066d365c8d531b9c0686bf))


### Bug Fixes

* answer once, keep sudo off the screen, read any language, take turns, trim pasted passwords ([#115](https://github.com/nearhost/nearhost/issues/115)) ([978fc75](https://github.com/nearhost/nearhost/commit/978fc75ae6bd00ac9bd62f7b535523f7053ec91d))
* fit the password box and questions to what they say ([#119](https://github.com/nearhost/nearhost/issues/119)) ([a99ad23](https://github.com/nearhost/nearhost/commit/a99ad2331ab6f44ccd72d17358492754be940355))
* keep Caddy's start from writing to the terminal ([#110](https://github.com/nearhost/nearhost/issues/110)) ([2fe1540](https://github.com/nearhost/nearhost/commit/2fe1540ec01c798256a63bdec53727023e27b236))
* say the CA is trusted so browsers accept HTTPS from Nearhost's web server ([#120](https://github.com/nearhost/nearhost/issues/120)) ([8baec0e](https://github.com/nearhost/nearhost/commit/8baec0e7e5b761f0b837e055dc229aeea545f459))
* say the first screen adds projects on this machine or a server ([#121](https://github.com/nearhost/nearhost/issues/121)) ([311ead0](https://github.com/nearhost/nearhost/commit/311ead09cede7d3d458f3d4ed275f26f1f227580))
* wait for the proxy's ports when its last site goes, as when it stops ([#117](https://github.com/nearhost/nearhost/issues/117)) ([5e183fb](https://github.com/nearhost/nearhost/commit/5e183fbb9c2fdda8f547797678061ee2a6a5513e))

## [0.3.0](https://github.com/nearhost/nearhost/compare/v0.2.0...v0.3.0) (2026-10-15)


### Features

* count a server's failed attempts and say when it tries next ([#97](https://github.com/nearhost/nearhost/issues/97)) ([c0ae45a](https://github.com/nearhost/nearhost/commit/c0ae45a80014a7a95cae97b798d870221fbc2e0b))
* draw the first-run screen with the mark in half blocks ([#91](https://github.com/nearhost/nearhost/issues/91)) ([07b6abf](https://github.com/nearhost/nearhost/commit/07b6abfcd1a1a693a6485b556fe2142e306f180f))
* draw the footer with bubbles help and list every key on ? ([#85](https://github.com/nearhost/nearhost/issues/85)) ([621f366](https://github.com/nearhost/nearhost/commit/621f3665d33f795e8d8beab48bc0d7165cbacb09))
* draw the header with the mark, rules and the compact layout ([#84](https://github.com/nearhost/nearhost/issues/84)) ([f4f5e8d](https://github.com/nearhost/nearhost/commit/f4f5e8d23cc07463a8ca04fd908afcd5674d9e3a))
* drop a server's connection and hold it, or connect it again, with c ([#98](https://github.com/nearhost/nearhost/issues/98)) ([e47f921](https://github.com/nearhost/nearhost/commit/e47f92181125e114e9a0bdc9ba0bd7cd48dc84a5))
* filter the sidebar by name with a text input ([#88](https://github.com/nearhost/nearhost/issues/88)) ([0524baf](https://github.com/nearhost/nearhost/commit/0524bafd7416f9ab3ff6d2dcc4a9d31fd001e31f))
* group the sidebar under its servers, fill the selected row and spin while starting ([#87](https://github.com/nearhost/nearhost/issues/87)) ([b2a4166](https://github.com/nearhost/nearhost/commit/b2a4166d206533d1f7e7309e6129487842d6bd14))
* keep each request and response in memory and show their bodies in the log ([#99](https://github.com/nearhost/nearhost/issues/99)) ([17c8a5b](https://github.com/nearhost/nearhost/commit/17c8a5b1476db8bc2459ebe2fcec0cc1bc807547))
* keep log entries whole and draw them in columns ([#86](https://github.com/nearhost/nearhost/issues/86)) ([9b3c0ff](https://github.com/nearhost/nearhost/commit/9b3c0ff2c98bff0e2295fc8f903eb8942e4c5997))
* lay out the project panel with its link, uptime, routes table and advice box ([#89](https://github.com/nearhost/nearhost/issues/89)) ([7a1e62b](https://github.com/nearhost/nearhost/commit/7a1e62b6c2b21a91b45d864f2847b439aabe8901))
* list problems on home with their fixes, and restyle the server panel ([#90](https://github.com/nearhost/nearhost/issues/90)) ([df6dc78](https://github.com/nearhost/nearhost/commit/df6dc789191cb9f8686d032a46dd4f7c3fb61e05))
* name the presets that apply to a project in its panel ([#96](https://github.com/nearhost/nearhost/issues/96)) ([979564e](https://github.com/nearhost/nearhost/commit/979564ee7b8a07f0ea77ec330b984ee859b77657))
* open a row's log full screen with kinds, search, follow and opened requests ([#94](https://github.com/nearhost/nearhost/issues/94)) ([934edbd](https://github.com/nearhost/nearhost/commit/934edbd5de2756c7679fec06f798ff5b0cda08de))
* open forms in a box over the screen drawn faint ([#92](https://github.com/nearhost/nearhost/issues/92)) ([1f7c6e4](https://github.com/nearhost/nearhost/commit/1f7c6e427da7573b8ed08f30440621436e210acb))
* send a kept request again from the log, asking first unless it is safe ([#100](https://github.com/nearhost/nearhost/issues/100)) ([ab2f294](https://github.com/nearhost/nearhost/commit/ab2f294ed329376892b81e2f410d7c758adba65c))
* show when the local CA expires, on home and in doctor ([#95](https://github.com/nearhost/nearhost/issues/95)) ([4c6acae](https://github.com/nearhost/nearhost/commit/4c6acae022ad390b7f083d173499eddd68af2f13))
* tick routes in the add form, choose roles with arrows and show earlier answers ([#93](https://github.com/nearhost/nearhost/issues/93)) ([5bdd11a](https://github.com/nearhost/nearhost/commit/5bdd11ae19e01e41fc4bfd100288e8435f9d4300))


### Bug Fixes

* decode zlib deflate, mark bodies decoded in part, and decode an opened request once ([#105](https://github.com/nearhost/nearhost/issues/105)) ([4f75c13](https://github.com/nearhost/nearhost/commit/4f75c1393f7981179b9abb8a46e9207e0f7e3e8b))
* footer clicks on focused routes, advice once the selection rests, refusals on their row, first-run and rename details ([#82](https://github.com/nearhost/nearhost/issues/82)) ([8dedc21](https://github.com/nearhost/nearhost/commit/8dedc21ced52fcc593cd93f6314c51235c155c0c))
* keep a request body safely, keep dropped servers dropped across reloads, clean replay errors ([#101](https://github.com/nearhost/nearhost/issues/101)) ([13a3ab7](https://github.com/nearhost/nearhost/commit/13a3ab78b53b4d807ffe6bcdd42b3c915a558a51))
* keep no exchange after the store is cleared, and count headers and room against its limit ([#104](https://github.com/nearhost/nearhost/issues/104)) ([77f8b40](https://github.com/nearhost/nearhost/commit/77f8b4049a947a5f316021e5cf78d4d5394669fc))
* replay an absolute-form URI by its path, drop headers its Connection header names, and say OPTIONS goes at once ([#106](https://github.com/nearhost/nearhost/issues/106)) ([a7bd11a](https://github.com/nearhost/nearhost/commit/a7bd11a628a0fdc4e687fe3da56b5de939f31a3e))
* wait for the proxy's ports to be released when it stops ([#103](https://github.com/nearhost/nearhost/issues/103)) ([d71f950](https://github.com/nearhost/nearhost/commit/d71f9503e7abe34852dd6acff4598af27048c98e))

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
