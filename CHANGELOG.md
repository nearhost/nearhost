# Changelog

## [0.14.2](https://github.com/nearhost/nearhost/compare/v0.14.1...v0.14.2) (2026-11-21)


### Bug Fixes

* a run directory too long for ssh gets advice that fixes it ([#515](https://github.com/nearhost/nearhost/issues/515)) ([c51182b](https://github.com/nearhost/nearhost/commit/c51182bcc5376888e41f5f97fb45e4e322a04bc8))
* **cli:** enter asks for the password again after a refused hosts update ([#531](https://github.com/nearhost/nearhost/issues/531)) ([0622a76](https://github.com/nearhost/nearhost/commit/0622a76da417c89fec606d2d27938736e6e7c3b9))
* **config:** a read of the id file waits out a sharing violation on Windows ([#536](https://github.com/nearhost/nearhost/issues/536)) ([7c05203](https://github.com/nearhost/nearhost/commit/7c05203ba3616365465a9f0c9882094bb90c7283))
* **engine:** a passthrough leaves the loopback routes' ports alone ([#514](https://github.com/nearhost/nearhost/issues/514)) ([dada512](https://github.com/nearhost/nearhost/commit/dada512fe4878b0402d16c716c9f8d4a0b7b3164))
* **proxy:** a load waits for the certificates of the hosts it adds ([#529](https://github.com/nearhost/nearhost/issues/529)) ([46b34d6](https://github.com/nearhost/nearhost/commit/46b34d69d4deeb4c9af7217a11572d4dc4707f67))
* **proxy:** a stop waits for its listeners to close ([#516](https://github.com/nearhost/nearhost/issues/516)) ([2846ea4](https://github.com/nearhost/nearhost/commit/2846ea4d0ce69f01fe3dd23926fbdad86a71cbec))

## [0.14.1](https://github.com/nearhost/nearhost/compare/v0.14.0...v0.14.1) (2026-11-20)


### Bug Fixes

* **documents:** a file on a server whose folder is being written is checked again ([#513](https://github.com/nearhost/nearhost/issues/513)) ([f639a56](https://github.com/nearhost/nearhost/commit/f639a56238ead4929c170d04cda4cc72b4a6581e))
* **engine:** a web forward leaves the loopback routes' ports alone ([#506](https://github.com/nearhost/nearhost/issues/506)) ([46869c0](https://github.com/nearhost/nearhost/commit/46869c004f29b83c91ffe16bba39bd1508fa5a74))
* **proxy:** a load that fails waits for the listeners it had opened ([#501](https://github.com/nearhost/nearhost/issues/501)) ([9f0532a](https://github.com/nearhost/nearhost/commit/9f0532a0197afa7c5b1bfbd6d3e280ebad776a11))
* **proxy:** a reload waits for the old listeners to close ([#502](https://github.com/nearhost/nearhost/issues/502)) ([bb2c3e7](https://github.com/nearhost/nearhost/commit/bb2c3e7a9cb2e93f657fa9a227efba23658bc912))

## [0.14.0](https://github.com/nearhost/nearhost/compare/v0.13.0...v0.14.0) (2026-11-19)


### Features

* **proxy:** Nearhost's page is at nearhost.localhost ([#491](https://github.com/nearhost/nearhost/issues/491)) ([9428ad9](https://github.com/nearhost/nearhost/commit/9428ad989c729c5405fd7814a0fa2052a3a4b526))
* **proxy:** Nearhost's page is served on its own port only ([#490](https://github.com/nearhost/nearhost/issues/490)) ([2d1e57b](https://github.com/nearhost/nearhost/commit/2d1e57b85658a9e901e5160280b07b1774a2196f))
* **trust:** untrust --delete-key removes the local CA's key ([#446](https://github.com/nearhost/nearhost/issues/446)) ([ad03d8f](https://github.com/nearhost/nearhost/commit/ad03d8fe0ed919d7774626b3225bacec03d866dc))


### Bug Fixes

* **add:** a lone port variable that becomes the web route gets a note ([#411](https://github.com/nearhost/nearhost/issues/411)) ([077800c](https://github.com/nearhost/nearhost/commit/077800c87436cc2c4a803a157c8460025280cb7a))
* **add:** add says whether the running nearhost picks the project up ([#468](https://github.com/nearhost/nearhost/issues/468)) ([8f795b8](https://github.com/nearhost/nearhost/commit/8f795b888101c652a2a57c92f18cbbb79c96c582))
* **add:** approval asks about each host and says what approving does ([#388](https://github.com/nearhost/nearhost/issues/388)) ([fb8a67e](https://github.com/nearhost/nearhost/commit/fb8a67e83fac67455e2c9035230a0037edd64962))
* **add:** ctrl-c during add leaves no temp folder ([#480](https://github.com/nearhost/nearhost/issues/480)) ([f60e1a8](https://github.com/nearhost/nearhost/commit/f60e1a8783705c80ece376ed9b61182ef3b6bc92))
* **agentapi:** a request you refused cannot be asked again for 30 seconds ([#394](https://github.com/nearhost/nearhost/issues/394)) ([7d7ea23](https://github.com/nearhost/nearhost/commit/7d7ea2346ef96accc8c32521bb76f1aad5ec4301))
* **agentapi:** a server can hold at most 16 passthroughs open ([#396](https://github.com/nearhost/nearhost/issues/396)) ([c6fdf3b](https://github.com/nearhost/nearhost/commit/c6fdf3b668d73aa376bf94a3d354ebc38d73e13e))
* **agentapi:** documents and doctor calls are limited per server ([#400](https://github.com/nearhost/nearhost/issues/400)) ([dc58b2b](https://github.com/nearhost/nearhost/commit/dc58b2bcf621ae5b32ebb1a98c56b66ddcaf803b))
* **agents:** the skill asks you before it runs nearhost add ([#414](https://github.com/nearhost/nearhost/issues/414)) ([47bc1c7](https://github.com/nearhost/nearhost/commit/47bc1c76b78e3814488b7c591d80f59aaebdc299))
* build with go 1.27.4, which fixes an http server crash in the standard library ([#415](https://github.com/nearhost/nearhost/issues/415)) ([573d942](https://github.com/nearhost/nearhost/commit/573d942500f71f0d531974d180474661a0351d08))
* **cli:** a --config file that does not exist is named ([#474](https://github.com/nearhost/nearhost/issues/474)) ([3745973](https://github.com/nearhost/nearhost/commit/37459733550de566472acba0de58dfc85f4dfd87))
* **cli:** a mistyped subcommand under preset or hosts is an error ([#472](https://github.com/nearhost/nearhost/issues/472)) ([021d46b](https://github.com/nearhost/nearhost/commit/021d46b197d13ef52bcd97bb394ceff51f2bab18))
* **cli:** is a Nearhost running asks the instance lock first ([#496](https://github.com/nearhost/nearhost/issues/496)) ([dec1fe3](https://github.com/nearhost/nearhost/commit/dec1fe3d7c03f431c54c2e228561f6b7681b33f7))
* **cli:** nearhost without a terminal says to use nearhost up ([#471](https://github.com/nearhost/nearhost/issues/471)) ([9b16cd8](https://github.com/nearhost/nearhost/commit/9b16cd8359737414bd6795f63d5d7b1d56d3a7c6))
* **cli:** small fixes from the final review ([#489](https://github.com/nearhost/nearhost/issues/489)) ([bb45b5a](https://github.com/nearhost/nearhost/commit/bb45b5a53b7a36067bfa95864d6ae040814c95df))
* **config:** a key Nearhost does not know stops a save instead of vanishing ([#404](https://github.com/nearhost/nearhost/issues/404)) ([d6dca5f](https://github.com/nearhost/nearhost/commit/d6dca5f163b5a5c40900acdbb2d6fb374824e5fb))
* **config:** a read-only file on windows is reported as read-only ([#488](https://github.com/nearhost/nearhost/issues/488)) ([9728d5a](https://github.com/nearhost/nearhost/commit/9728d5ac7fb8e0b98e24c84c867e6a1c91db082e))
* **config:** an empty id file is replaced, not fatal ([#419](https://github.com/nearhost/nearhost/issues/419)) ([9587527](https://github.com/nearhost/nearhost/commit/95875276d1a25701a0757018e68768a8a6fa1d73))
* **config:** an exact host under another project's wildcard is a clash ([#417](https://github.com/nearhost/nearhost/issues/417)) ([06c0352](https://github.com/nearhost/nearhost/commit/06c035290679301168e1e05ce8650a405a1e7bae))
* **config:** saves survive a crash, keep the file's mode and write through a symlink ([#403](https://github.com/nearhost/nearhost/issues/403)) ([44be66b](https://github.com/nearhost/nearhost/commit/44be66b870ed04fc7a86d771e6eac39a5d55d39f))
* **config:** the CA folder is private on windows wherever it lives ([#448](https://github.com/nearhost/nearhost/issues/448)) ([efdb8af](https://github.com/nearhost/nearhost/commit/efdb8af5296ce4229c3d5a34b369982965585883))
* **desktop:** opening the browser returns at once ([#413](https://github.com/nearhost/nearhost/issues/413)) ([92e601f](https://github.com/nearhost/nearhost/commit/92e601f02763c24f61d8e499c1ee374d5d47e680))
* **discovery:** a .env that cannot be read is named by line, not printed ([#395](https://github.com/nearhost/nearhost/issues/395)) ([200b2c3](https://github.com/nearhost/nearhost/commit/200b2c3e74ed5eae8486d60bd642c8aad67a30bb))
* **discovery:** compose ports come from the override file too ([#409](https://github.com/nearhost/nearhost/issues/409)) ([45f064b](https://github.com/nearhost/nearhost/commit/45f064bfeb79c943076cee0e49e6e92d35b8f428))
* **discovery:** project files are read up to 1 MiB and only when regular ([#399](https://github.com/nearhost/nearhost/issues/399)) ([537aefb](https://github.com/nearhost/nearhost/commit/537aefbedc4ee01c08ce46f338ebcdfaaf975cd8))
* **dockernet:** only local bridge networks count as docker clients ([#420](https://github.com/nearhost/nearhost/issues/420)) ([8a70c60](https://github.com/nearhost/nearhost/commit/8a70c60963c020e27540f6bba698168117d9e7e9))
* **doctor:** doctor warns when ssh on windows is not Microsoft's ([#476](https://github.com/nearhost/nearhost/issues/476)) ([14dcf63](https://github.com/nearhost/nearhost/commit/14dcf6332c4bcb644dd50bfecbb3b6628beed06d))
* **doctor:** each check prints as it finishes ([#475](https://github.com/nearhost/nearhost/issues/475)) ([0a6ed46](https://github.com/nearhost/nearhost/commit/0a6ed463da4dca589bd3a01903f5579ffacdec34))
* **doctor:** ports held by the running nearhost are fine ([#467](https://github.com/nearhost/nearhost/issues/467)) ([4d68ea5](https://github.com/nearhost/nearhost/commit/4d68ea56e98064e9f16a9c11c5fd670879adb9fe))
* **doctor:** probe the --web-port where the page listens ([#498](https://github.com/nearhost/nearhost/issues/498)) ([2d18d6d](https://github.com/nearhost/nearhost/commit/2d18d6d4df29b23fed27e654c0f68a5019d28bbb))
* **doctor:** the ssh vendor check names the ssh Nearhost runs ([#494](https://github.com/nearhost/nearhost/issues/494)) ([94b9436](https://github.com/nearhost/nearhost/commit/94b94369c02ff86aadc41f1fa2db925972b8988c))
* **documents:** a file is checked after it is opened, not before ([#412](https://github.com/nearhost/nearhost/issues/412)) ([9d24652](https://github.com/nearhost/nearhost/commit/9d2465274fd36e891411e7639913a84aed980d7f))
* **documents:** a page's script can load assets, not read other files ([#402](https://github.com/nearhost/nearhost/issues/402)) ([29c2b84](https://github.com/nearhost/nearhost/commit/29c2b848dfb82cf71919bc773cec6cb7b9b6ee58))
* **documents:** a tree inside a hidden folder serves only documents ([#405](https://github.com/nearhost/nearhost/issues/405)) ([d522eb1](https://github.com/nearhost/nearhost/commit/d522eb11ca8cd29274c286920018aa86c968e47b))
* **documents:** always allow covers the folders you have approved ([#407](https://github.com/nearhost/nearhost/issues/407)) ([f8ca298](https://github.com/nearhost/nearhost/commit/f8ca2981950b2664eb178de23fc3118a4566c52d))
* **elevate:** a cancelled windows prompt keeps its turn until it closes ([#449](https://github.com/nearhost/nearhost/issues/449)) ([8938486](https://github.com/nearhost/nearhost/commit/893848629d474ffd6861d276e7144a07dfeb1fa9))
* **elevate:** running as root updates the hosts file without sudo ([#434](https://github.com/nearhost/nearhost/issues/434)) ([ece72d1](https://github.com/nearhost/nearhost/commit/ece72d1138cb68ab96cfe4b7b495bf383a938556))
* **engine:** a project waits for its server and starts when it connects ([#408](https://github.com/nearhost/nearhost/issues/408)) ([9357c85](https://github.com/nearhost/nearhost/commit/9357c85401216b668a76d978e2638d0a7e4a9a3e))
* **engine:** a second nearhost with the same config says so and stops ([#393](https://github.com/nearhost/nearhost/issues/393)) ([081fc00](https://github.com/nearhost/nearhost/commit/081fc00b5e2a4438d26029d47b74af461219cf67))
* **engine:** a site whose forward is down answers 502 without dialling ([#443](https://github.com/nearhost/nearhost/issues/443)) ([3045424](https://github.com/nearhost/nearhost/commit/3045424b498311a2d65019153bb663ade8201063))
* **engine:** ssh masters left by a crash end at the next start ([#397](https://github.com/nearhost/nearhost/issues/397)) ([a4698d5](https://github.com/nearhost/nearhost/commit/a4698d5476956db0621435542db123007dcd31e9))
* **engine:** the already-running message does not offer a second nearhost ([#484](https://github.com/nearhost/nearhost/issues/484)) ([8bb3f3c](https://github.com/nearhost/nearhost/commit/8bb3f3c554c7e47c371bedf8513af21ee2429a1e))
* **engine:** the port check on macOS sees a server bound to loopback ([#473](https://github.com/nearhost/nearhost/issues/473)) ([63c7461](https://github.com/nearhost/nearhost/commit/63c746130233d6be5d76121a0e52efe4b7fcd448))
* **hosts:** a read-only hosts file on windows says how to clear it ([#435](https://github.com/nearhost/nearhost/issues/435)) ([2012f6d](https://github.com/nearhost/nearhost/commit/2012f6d04e5e7e2b795c72ea85c0cf19c4996ae7))
* **hosts:** the block uses the hosts file's line endings ([#438](https://github.com/nearhost/nearhost/issues/438)) ([6af82ba](https://github.com/nearhost/nearhost/commit/6af82ba406ab413336d141c8ee6283f30fb498fe))
* **hosts:** the DNS check reads systemd-resolved's upstream servers ([#392](https://github.com/nearhost/nearhost/issues/392)) ([49d6d2b](https://github.com/nearhost/nearhost/commit/49d6d2bc9fa1a334886592b32a8b447b2b7cb3d2))
* **hosts:** the elevated helper reads and reports only where its caller asked from ([#431](https://github.com/nearhost/nearhost/issues/431)) ([5aafbb0](https://github.com/nearhost/nearhost/commit/5aafbb0cb712034a507202181232b565e74f7d58))
* **hosts:** the elevated helper refuses any file but the hosts file ([#426](https://github.com/nearhost/nearhost/issues/426)) ([20b1912](https://github.com/nearhost/nearhost/commit/20b191246dc83ef992d998d117cf9a092c472638))
* **nearhostrc:** a loopback port a project file adds asks first ([#381](https://github.com/nearhost/nearhost/issues/381)) ([d6a6b55](https://github.com/nearhost/nearhost/commit/d6a6b553050f237a017e3f2345e65473e851663a))
* **nearhostrc:** labels and error text lose their control characters ([#406](https://github.com/nearhost/nearhost/issues/406)) ([0d0183f](https://github.com/nearhost/nearhost/commit/0d0183f870f8d0cd41dd369af4e3d9c0118fffaf))
* **nearhostrc:** only ${NAME} is a variable in a project file ([#410](https://github.com/nearhost/nearhost/issues/410)) ([7697105](https://github.com/nearhost/nearhost/commit/76971055e8738b6d7b946a4f28ee7cd1658fc45b))
* **proxy:** a page port below 1024 on macOS still binds ([#482](https://github.com/nearhost/nearhost/issues/482)) ([5b0d32d](https://github.com/nearhost/nearhost/commit/5b0d32d43ca95c4e2e77606d61f8069d212c30b1))
* **proxy:** a request body the app stopped reading is marked and not sent again ([#462](https://github.com/nearhost/nearhost/issues/462)) ([4b8be32](https://github.com/nearhost/nearhost/commit/4b8be32f46a743678e0e1e191ad5f1d831cb4db0))
* **proxy:** a site whose app is down says so ([#433](https://github.com/nearhost/nearhost/issues/433)) ([0552ec6](https://github.com/nearhost/nearhost/commit/0552ec6e75053e21a0ae6b32ad756c73e00aab04))
* **proxy:** a stop that fails can be tried again ([#470](https://github.com/nearhost/nearhost/issues/470)) ([d147a33](https://github.com/nearhost/nearhost/commit/d147a338cc7a75b5bd518d736e403e7b0c2e2dc5))
* **proxy:** an http/3 request with no body is not marked as cut short ([#481](https://github.com/nearhost/nearhost/issues/481)) ([1a57096](https://github.com/nearhost/nearhost/commit/1a570967732437bcb8f3fcf7f3bb235b15caae6b))
* **proxy:** expired certificates for pattern hosts are removed at start ([#464](https://github.com/nearhost/nearhost/issues/464)) ([f630803](https://github.com/nearhost/nearhost/commit/f630803f7b8403dd9792da6193fba78cc9a5d1af))
* **proxy:** Nearhost's page listens on loopback only ([#456](https://github.com/nearhost/nearhost/issues/456)) ([163fc16](https://github.com/nearhost/nearhost/commit/163fc167838737e781cabf704afcf4132eaa63c0))
* **proxy:** Nearhost's page lists documents only to your browser ([#458](https://github.com/nearhost/nearhost/issues/458)) ([01c73eb](https://github.com/nearhost/nearhost/commit/01c73eb23644096d91586e1c9be56653c11684bd))
* **proxy:** site certificates last a week and renew after the laptop wakes ([#450](https://github.com/nearhost/nearhost/issues/450)) ([fcfbad3](https://github.com/nearhost/nearhost/commit/fcfbad33bf39ccf20f64990635a9c5908c032d70))
* **proxy:** the redirect to https is a 307 browsers do not keep ([#453](https://github.com/nearhost/nearhost/issues/453)) ([1c7ad1b](https://github.com/nearhost/nearhost/commit/1c7ad1b0f8b587eb21350aa1284ec8b724e15ee9))
* **proxy:** the redirect to https keeps a port other than 443 ([#454](https://github.com/nearhost/nearhost/issues/454)) ([af0ace7](https://github.com/nearhost/nearhost/commit/af0ace7e7eddd4ce0a4b5964296932b0d6b9db50))
* **proxy:** zstd bodies decode with an 8 MiB window ([#466](https://github.com/nearhost/nearhost/issues/466)) ([8d4fd8c](https://github.com/nearhost/nearhost/commit/8d4fd8c6aaf06b5b9d6266d1393a1e4ac6f52079))
* **sudo:** a wrong password under sudo-rs offers another try ([#452](https://github.com/nearhost/nearhost/issues/452)) ([a64d5f5](https://github.com/nearhost/nearhost/commit/a64d5f5bdddaf84516e4c376a87e7c5b43c1ff5c))
* **transport:** a forward's port check on windows sees another holder ([#421](https://github.com/nearhost/nearhost/issues/421)) ([4921506](https://github.com/nearhost/nearhost/commit/49215069dd0f7aacd8ac90a9bef613333de1c0af))
* **transport:** a killed nearhost takes its ssh.exe children with it on windows ([#386](https://github.com/nearhost/nearhost/issues/386)) ([932c2df](https://github.com/nearhost/nearhost/commit/932c2df67e63bf93600290785ef48bbbcb4bd6e3))
* **transport:** a lingering proxy command no longer holds reconnect or quit ([#398](https://github.com/nearhost/nearhost/issues/398)) ([84b962e](https://github.com/nearhost/nearhost/commit/84b962e7d4fcf2f47babadaa29b7fe7d28823954))
* **transport:** a refused login is retried every 15 minutes ([#416](https://github.com/nearhost/nearhost/issues/416)) ([2ef1384](https://github.com/nearhost/nearhost/commit/2ef13847d9711bc11f35dbd2b92c2e41e7f58dfe))
* **transport:** ssh is found once at start and run by its path ([#428](https://github.com/nearhost/nearhost/issues/428)) ([553e595](https://github.com/nearhost/nearhost/commit/553e59503f8a1544e1a4827684a2f27167c82f46))
* **transport:** the master transport turns ForkAfterAuthentication off ([#418](https://github.com/nearhost/nearhost/issues/418)) ([d7d1ca2](https://github.com/nearhost/nearhost/commit/d7d1ca205a44d875d6248e3e1e81d876e2733473))
* **transport:** the ssh master's last lines are its error, not its whole log ([#401](https://github.com/nearhost/nearhost/issues/401)) ([c11e09c](https://github.com/nearhost/nearhost/commit/c11e09c35377511421692af840c58dbfeeb52abb))
* **trust:** untrust says when the CA was not trusted and names stores it could not clean ([#442](https://github.com/nearhost/nearhost/issues/442)) ([8ae2fbd](https://github.com/nearhost/nearhost/commit/8ae2fbd27460855a871998767390ce148ff29f7e))
* **tui:** "CA not trusted" clears once you trust it on linux ([#430](https://github.com/nearhost/nearhost/issues/430)) ([72b5901](https://github.com/nearhost/nearhost/commit/72b590153e128f7a7830d0b2ed4e6268e763ed58))
* **tui:** a single click opens Nearhost's links; log text needs a double click ([#439](https://github.com/nearhost/nearhost/issues/439)) ([7100ab6](https://github.com/nearhost/nearhost/commit/7100ab65392d21806af2a397d026478c29b7b354))
* **tui:** a window too small to draw only takes quit ([#427](https://github.com/nearhost/nearhost/issues/427)) ([ace749c](https://github.com/nearhost/nearhost/commit/ace749c41b8a138383dac0a80760992d029e3fcf))
* **tui:** add project completes windows paths ([#429](https://github.com/nearhost/nearhost/issues/429)) ([ad6c758](https://github.com/nearhost/nearhost/commit/ad6c758428f6383294709e84642b34c7d74f1e69))
* **tui:** add server suggests hosts from included ssh config files ([#451](https://github.com/nearhost/nearhost/issues/451)) ([0727426](https://github.com/nearhost/nearhost/commit/0727426bfe2d5e76d56a212bc89d1926dc2dbfb2))
* **tui:** an agent's request ignores keys for a moment and always-allow is A ([#389](https://github.com/nearhost/nearhost/issues/389)) ([e158653](https://github.com/nearhost/nearhost/commit/e158653015be207d3cbddf876ae45f09157af03e))
* **tui:** copy as curl quotes for the shell you paste into ([#385](https://github.com/nearhost/nearhost/issues/385)) ([60e689f](https://github.com/nearhost/nearhost/commit/60e689f96f17b435c13258b67ef3553463c03473))
* **tui:** error and pending have their own marks ([#432](https://github.com/nearhost/nearhost/issues/432)) ([c3bc177](https://github.com/nearhost/nearhost/commit/c3bc1770b05f7c9a753ddeddf4b927bc16f14699))
* **tui:** esc cancels the password box while it checks ([#447](https://github.com/nearhost/nearhost/issues/447)) ([bfcd54d](https://github.com/nearhost/nearhost/commit/bfcd54d59e0f50a22ef1db8e12c9597bd25b4f07))
* **tui:** form fields move the cursor and edit in place ([#440](https://github.com/nearhost/nearhost/issues/440)) ([197e339](https://github.com/nearhost/nearhost/commit/197e3392ee48586aea70c64ca6250ea7ba1f4dbe))
* **tui:** no approve-again advice for a read-only hosts file ([#495](https://github.com/nearhost/nearhost/issues/495)) ([861405f](https://github.com/nearhost/nearhost/commit/861405f3c9d0541958edb96c8aa6532a7597ce1e))
* **tui:** p typed into the log's search stays in the search ([#477](https://github.com/nearhost/nearhost/issues/477)) ([036d16f](https://github.com/nearhost/nearhost/commit/036d16f4b0c855962ef82645e944482dd782a1fa))
* **tui:** quitting does not wait for a form's ssh work ([#436](https://github.com/nearhost/nearhost/issues/436)) ([f892a6f](https://github.com/nearhost/nearhost/commit/f892a6fafd50c82708d7593f5da88c6b07d3bdc0))
* **tui:** r on Home starts the proxy again after a port clash ([#445](https://github.com/nearhost/nearhost/issues/445)) ([d15fe20](https://github.com/nearhost/nearhost/commit/d15fe20461dfa00e30e83e612ab070e2d20f160f))
* **tui:** r retries the proxy from the first-run screen too ([#478](https://github.com/nearhost/nearhost/issues/478)) ([a4626df](https://github.com/nearhost/nearhost/commit/a4626dfe65719ba1b157dcbc0454e1658adc219e))
* **tui:** route focus clears when the selection jumps to a new project ([#391](https://github.com/nearhost/nearhost/issues/391)) ([b90c48e](https://github.com/nearhost/nearhost/commit/b90c48e95f5a151a5c4a7bf2789432c1b2089cf9))
* **tui:** small fixes from the final review ([#479](https://github.com/nearhost/nearhost/issues/479)) ([c5c5d53](https://github.com/nearhost/nearhost/commit/c5c5d53e9c59211e12dca095cc2a7d91dd267f46))
* **tui:** the interface is readable on a light terminal ([#422](https://github.com/nearhost/nearhost/issues/422)) ([fc02df4](https://github.com/nearhost/nearhost/commit/fc02df4eb9a4b1630628f4bf626c48e7205b5cb0))
* **tui:** the log view lists its keys, paste reaches the filter, esc closes welcome ([#437](https://github.com/nearhost/nearhost/issues/437)) ([744f04e](https://github.com/nearhost/nearhost/commit/744f04eeae2b9c2cb23398ab72d30582586220a9))
* **tui:** the ssh host list scrolls with the selection ([#441](https://github.com/nearhost/nearhost/issues/441)) ([86632cc](https://github.com/nearhost/nearhost/commit/86632ccb510f65bb4530cdd7f77b803dcd320840))
* **ui:** closing the terminal shuts the interface down ([#384](https://github.com/nearhost/nearhost/issues/384)) ([929623c](https://github.com/nearhost/nearhost/commit/929623c7a32f349aa391139f450dbf2297109742))
* **up:** a port nearhost may not bind on linux comes with the command that fixes it ([#465](https://github.com/nearhost/nearhost/issues/465)) ([abe6df6](https://github.com/nearhost/nearhost/commit/abe6df69835fa5c4d2091dadddf61038b8075509))
* **update:** --check exits 2 when it cannot check, and end of input is a no ([#461](https://github.com/nearhost/nearhost/issues/461)) ([838cf27](https://github.com/nearhost/nearhost/commit/838cf275f11f94fd77c9e12617d607df9db70df0))
* **update:** a release that has no files yet is not offered ([#460](https://github.com/nearhost/nearhost/issues/460)) ([976385a](https://github.com/nearhost/nearhost/commit/976385a39fe382bffb8491e354148d3d7d595b3a))
* **update:** a script update that did not install says so ([#457](https://github.com/nearhost/nearhost/issues/457)) ([1b81eb6](https://github.com/nearhost/nearhost/commit/1b81eb6b180a54fe3a4ddc7bdf353bb00b72441b))
* **update:** every linux update says how to let nearhost bind ports 80 and 443 again ([#459](https://github.com/nearhost/nearhost/issues/459)) ([fa2a61a](https://github.com/nearhost/nearhost/commit/fa2a61ac3631b0666638efb04084fcef066861e5))
* **update:** moving the running copy aside waits out a file that is briefly held ([#469](https://github.com/nearhost/nearhost/issues/469)) ([c3fa5db](https://github.com/nearhost/nearhost/commit/c3fa5db59585f93b7a9e08c97b25aa2ab53b7502))
* **update:** the windows update downloads first, then swaps ([#455](https://github.com/nearhost/nearhost/issues/455)) ([417a45c](https://github.com/nearhost/nearhost/commit/417a45c24d7c4e4fd8d1d5b7e0758ad5574ced67))
* **up:** naming a project is not refused over one this run does not start ([#486](https://github.com/nearhost/nearhost/issues/486)) ([fd8c186](https://github.com/nearhost/nearhost/commit/fd8c186e45498038c6a1ca3cc513d49ce4d53d57))
* **up:** up exits when nothing can be served ([#463](https://github.com/nearhost/nearhost/issues/463)) ([689f0bc](https://github.com/nearhost/nearhost/commit/689f0bcef7aceea692c20985005374535c02b27e))
* **up:** up keeps going while a refused server will try again ([#493](https://github.com/nearhost/nearhost/issues/493)) ([433693c](https://github.com/nearhost/nearhost/commit/433693cbde8f40c22ca1ce1c846c0a4500b88367))
* **up:** without a terminal, up gives one piece of advice ([#485](https://github.com/nearhost/nearhost/issues/485)) ([6dc5049](https://github.com/nearhost/nearhost/commit/6dc50498a37b08aa7a69d3f696ca02e95b7dee32))


### Performance Improvements

* **tui:** the full-screen log renders the rows on screen ([#444](https://github.com/nearhost/nearhost/issues/444)) ([4e66204](https://github.com/nearhost/nearhost/commit/4e6620417d9d692dfcecb716e5da59c405a62749))

## [0.13.0](https://github.com/nearhost/nearhost/compare/v0.12.0...v0.13.0) (2026-11-17)


### Features

* windows support ([#373](https://github.com/nearhost/nearhost/issues/373)) ([69243df](https://github.com/nearhost/nearhost/commit/69243dfb25505ccc45e6697cf2ffbb3cd782641d))

## [0.12.0](https://github.com/nearhost/nearhost/compare/v0.11.0...v0.12.0) (2026-11-17)


### Features

* a per-process ssh link that needs no controlmaster ([#334](https://github.com/nearhost/nearhost/issues/334)) ([ecd6fd9](https://github.com/nearhost/nearhost/commit/ecd6fd96067168d0e8d3aeffa298d7fe779439ca))
* a powershell skill script for windows laptops ([#350](https://github.com/nearhost/nearhost/issues/350)) ([17dd643](https://github.com/nearhost/nearhost/commit/17dd643f5630d148d4aa2729789b4fbd4cd67432))
* coding agents on servers reach nearhost on windows through a token-checked loopback listener ([#347](https://github.com/nearhost/nearhost/issues/347)) ([1b9c4db](https://github.com/nearhost/nearhost/commit/1b9c4dbead3a988f2cd599ea31db9fcf0221a20a))
* connect a server from the first-run screen ([#329](https://github.com/nearhost/nearhost/issues/329)) ([673eaef](https://github.com/nearhost/nearhost/commit/673eaefd0a31d7478ca63944661e05e3365595d4))
* doctor checks ssh.exe, proxyjump aliases and firefox's roots on windows ([#349](https://github.com/nearhost/nearhost/issues/349)) ([4582d7d](https://github.com/nearhost/nearhost/commit/4582d7d2324c0a610031f94a2e0e71cb26add150))
* doctor names http.sys on port 80 and checks the ssh agent service on windows ([#348](https://github.com/nearhost/nearhost/issues/348)) ([0ba24b2](https://github.com/nearhost/nearhost/commit/0ba24b24efb12d3797c76916541be2f1930c056f))
* find docker desktop's named pipe on windows ([#346](https://github.com/nearhost/nearhost/issues/346)) ([a502b1b](https://github.com/nearhost/nearhost/commit/a502b1b172ecd6002ba3dc6e4756260c5686aa5c))
* nearhost update replaces a running nearhost.exe on windows ([#372](https://github.com/nearhost/nearhost/issues/372)) ([5e6b0d4](https://github.com/nearhost/nearhost/commit/5e6b0d410efff2dc47c999fcd6fc7bf3524d3180))
* npm packages and launcher for windows ([#369](https://github.com/nearhost/nearhost/issues/369)) ([cc3f349](https://github.com/nearhost/nearhost/commit/cc3f34951973f7d97d61b6e25a485ae04a3363d1))
* on windows, manage the system hosts file and flush the dns cache after a change ([#344](https://github.com/nearhost/nearhost/issues/344)) ([5a8ce56](https://github.com/nearhost/nearhost/commit/5a8ce56886c36923596018c61061209be6337735))
* one ssh process per forward in the per-process transport ([#336](https://github.com/nearhost/nearhost/issues/336)) ([940de72](https://github.com/nearhost/nearhost/commit/940de7202357120b9a0a6b7833a480e9e2d670a0))
* prerelease builds update along the prerelease line, and windows gets its own update commands ([#371](https://github.com/nearhost/nearhost/issues/371)) ([0e7b72c](https://github.com/nearhost/nearhost/commit/0e7b72c5493d14eee230d3c997baad7a14bbafc4))
* read nameservers from the network adapters on windows ([#345](https://github.com/nearhost/nearhost/issues/345)) ([6e28de6](https://github.com/nearhost/nearhost/commit/6e28de697dac0efae967bf3f90af9a52edf60fb9))
* the per-process link carries the agent socket ([#335](https://github.com/nearhost/nearhost/issues/335)) ([2c19a93](https://github.com/nearhost/nearhost/commit/2c19a93d703a3c2d368a992e1727ce8b0cfe7b06))
* windows uses the per-process transport, and NEARHOST_TRANSPORT=per-process opts in elsewhere ([#337](https://github.com/nearhost/nearhost/issues/337)) ([aa094ca](https://github.com/nearhost/nearhost/commit/aa094ca0044b8f7964d625f4eddea5af75a65cf5))


### Bug Fixes

* a per-process forward that is reconnecting shows as degraded, not as a port in use ([#342](https://github.com/nearhost/nearhost/issues/342)) ([691a6ef](https://github.com/nearhost/nearhost/commit/691a6ef1790f52f281d8b23cbb31748b31ac7ce7))
* a release's npm step can be run again while npm is still processing ([#376](https://github.com/nearhost/nearhost/issues/376)) ([ad06a6b](https://github.com/nearhost/nearhost/commit/ad06a6bca348271624c376f550233b15269206fe))
* doctor notes, not warns, that docker clients need the opt-in on windows ([#326](https://github.com/nearhost/nearhost/issues/326)) ([9c05471](https://github.com/nearhost/nearhost/commit/9c0547142d0e1bb3601f3f146b9386a5b7fa9d21))
* name the windows certificate store and its prompt in trust and doctor ([#318](https://github.com/nearhost/nearhost/issues/318)) ([6555ce1](https://github.com/nearhost/nearhost/commit/6555ce1a2794cc713acc8d64f21279ec3b625b9c))
* nearhost update downloads the release itself on windows ([#377](https://github.com/nearhost/nearhost/issues/377)) ([42cf65b](https://github.com/nearhost/nearhost/commit/42cf65b4951c4274990ca936ec49ba14c6c32b77))
* per-process forwards say when they are being retried, and the link waits for the target's login ([#341](https://github.com/nearhost/nearhost/issues/341)) ([99948cf](https://github.com/nearhost/nearhost/commit/99948cf9219396a48100cd57466c26d895d7db48))
* put the agent socket in the server's runtime directory, where selinux lets sshd create it ([#332](https://github.com/nearhost/nearhost/issues/332)) ([ca6ae61](https://github.com/nearhost/nearhost/commit/ca6ae61f96ce8d9b8a1af17130711702aed920e2))
* say when a windows prompt was declined or went unanswered, and forward no agent socket from windows yet ([#338](https://github.com/nearhost/nearhost/issues/338)) ([708a03a](https://github.com/nearhost/nearhost/commit/708a03a8d9472d51c8d74e7393bb24b61b51c938))
* send held agent notices once the log has a subscriber, and flag a refused port on per-process forwards ([#343](https://github.com/nearhost/nearhost/issues/343)) ([7c747ef](https://github.com/nearhost/nearhost/commit/7c747ef0d0b7850f14ca1705766e63eb36d33799))
* serve on loopback only on windows, so no firewall prompt ([#317](https://github.com/nearhost/nearhost/issues/317)) ([346c0e8](https://github.com/nearhost/nearhost/commit/346c0e8e1a7fe165ac6ba3c3668e0c6ee87d05ad))
* skip the nss stores on windows, where firefox reads the windows store ([#319](https://github.com/nearhost/nearhost/issues/319)) ([db5d74b](https://github.com/nearhost/nearhost/commit/db5d74b72bde384721157c8a00b0d334270495c2))
* the skill walks through connecting a machine that no nearhost reaches ([#330](https://github.com/nearhost/nearhost/issues/330)) ([56cf6cd](https://github.com/nearhost/nearhost/commit/56cf6cd80be0a5226c1bb5157a318bcd42cf3da3))
* two windows proxy faults behind flaky tests ([#375](https://github.com/nearhost/nearhost/issues/375)) ([87b3399](https://github.com/nearhost/nearhost/commit/87b33991942cfa95c89a256e1a635635fb9892e0))
* windows run folder, proxyjump advice, token message and copy from the stage 5 review ([#352](https://github.com/nearhost/nearhost/issues/352)) ([4087c9f](https://github.com/nearhost/nearhost/commit/4087c9f494ba035835ff9c4f181b0cad1f4c5577))
* windows update gaps from the stage 7 review ([#374](https://github.com/nearhost/nearhost/issues/374)) ([9dcae42](https://github.com/nearhost/nearhost/commit/9dcae42c1908a5486497203f3b0ef15a4895e2e6))

## [0.11.0](https://github.com/nearhost/nearhost/compare/v0.10.0...v0.11.0) (2026-10-28)


### Features

* **agentapi:** let agents ask to show a document ([#293](https://github.com/nearhost/nearhost/issues/293)) ([6a6f85d](https://github.com/nearhost/nearhost/commit/6a6f85d745b34d2d27a72a007dbcffa6ad4f2859))
* ask in nearhost before showing an agent's document, and open it ([#294](https://github.com/nearhost/nearhost/issues/294)) ([5012b7b](https://github.com/nearhost/nearhost/commit/5012b7b0ffe3b88abfdaba1d2fcdaed6203078d9))
* **documents:** choose a document's tree and keep the served trees ([#287](https://github.com/nearhost/nearhost/issues/287)) ([f5aa89d](https://github.com/nearhost/nearhost/commit/f5aa89d7b8216278de2e66623b0f06d1a0bd6a4d))
* **documents:** read trees on servers over the session's sftp ([#291](https://github.com/nearhost/nearhost/issues/291)) ([1ec0b31](https://github.com/nearhost/nearhost/commit/1ec0b316d531c8d89461227531046ea8a73f9e1f))
* **documents:** render markdown documents in nearhost's style ([#290](https://github.com/nearhost/nearhost/issues/290)) ([bf319f5](https://github.com/nearhost/nearhost/commit/bf319f57c9ef1066346b6e6034f6ae94c72d8c27))
* **documents:** serve a tree's files live, sandboxed, without dotfiles ([#288](https://github.com/nearhost/nearhost/issues/288)) ([0c8c8bf](https://github.com/nearhost/nearhost/commit/0c8c8bfca6aff17a251f303fb14fa352e04ce556))
* **proxy:** serve agents' documents and list them on nearhost's page ([#292](https://github.com/nearhost/nearhost/issues/292)) ([0bcde0d](https://github.com/nearhost/nearhost/commit/0bcde0d960540fa0353750a8d6d82783dbcba696))
* **skill:** offer documents the agent wrote in the user's browser ([#296](https://github.com/nearhost/nearhost/issues/296)) ([7e19567](https://github.com/nearhost/nearhost/commit/7e195677d49a769acbffc2e3d3b823513d548bb6))
* **tui:** list served documents and grants of each kind on the server panel ([#295](https://github.com/nearhost/nearhost/issues/295)) ([a9f524f](https://github.com/nearhost/nearhost/commit/a9f524fed854e4b60fd246ac369a91c57f8a9056))


### Bug Fixes

* **documents:** cubic review fixes ([#299](https://github.com/nearhost/nearhost/issues/299)) ([6919487](https://github.com/nearhost/nearhost/commit/6919487ebfef02d91a8350b3efeecfcdfaaf5556))
* **documents:** serve typescript as text, and final review fixes ([#298](https://github.com/nearhost/nearhost/issues/298)) ([2f61071](https://github.com/nearhost/nearhost/commit/2f6107130df91f81e661b9da0e8fcdcdc83261b9))

## [0.10.0](https://github.com/nearhost/nearhost/compare/v0.9.1...v0.10.0) (2026-10-27)


### Features

* **agentapi:** let agents ask for temporary passthroughs ([#271](https://github.com/nearhost/nearhost/issues/271)) ([2372cfe](https://github.com/nearhost/nearhost/commit/2372cfef74f8765ca94c51b2beb92caa3a594f3f))
* **engine:** close passthroughs when their server or port goes away ([#270](https://github.com/nearhost/nearhost/issues/270)) ([4d95045](https://github.com/nearhost/nearhost/commit/4d950458712536a8dd0391651adf05d3c4c051d5))
* **engine:** open temporary passthroughs on a connected server ([#268](https://github.com/nearhost/nearhost/issues/268)) ([feb1dfd](https://github.com/nearhost/nearhost/commit/feb1dfdd0db1f43a2510ea536515c4f7dc0b7020))
* **grants:** keep always-allow grants as token hashes ([#267](https://github.com/nearhost/nearhost/issues/267)) ([8bd3135](https://github.com/nearhost/nearhost/commit/8bd31354db5163bb28471ffe1dd29cfc8ee3f44c))
* serve passthroughs to agents from the engine ([#272](https://github.com/nearhost/nearhost/issues/272)) ([c4abca5](https://github.com/nearhost/nearhost/commit/c4abca52a0d44874f1457048345171f39fbd0471))
* **skill:** open browser previews on the laptop from a server ([#275](https://github.com/nearhost/nearhost/issues/275)) ([5c2f104](https://github.com/nearhost/nearhost/commit/5c2f104f7c3de7dd2572111439bfe0b2249e3960))
* **tui:** ask before opening a port for an agent ([#273](https://github.com/nearhost/nearhost/issues/273)) ([f1b953a](https://github.com/nearhost/nearhost/commit/f1b953a98d869ed59b4a37318551b4f36d645708))
* **tui:** list, close and revoke passthroughs on the server panel ([#274](https://github.com/nearhost/nearhost/issues/274)) ([4c8dc88](https://github.com/nearhost/nearhost/commit/4c8dc88adbd831b6bad3765673682a93d91513ba))


### Bug Fixes

* **agentapi:** give each close its own time, and report what a failed close or grant leaves ([#282](https://github.com/nearhost/nearhost/issues/282)) ([7d947c6](https://github.com/nearhost/nearhost/commit/7d947c6d07a821b8d80e92ed03484ec7b4850c78))
* **engine:** keep a closing passthrough until its cancel ends, and wait for passthroughs at shutdown ([#280](https://github.com/nearhost/nearhost/issues/280)) ([89e3412](https://github.com/nearhost/nearhost/commit/89e341240ba8ab300d6e5c9d4bb943fb4f14c184))
* **grants:** sync saves, keep a symlinked file's link, and prune removed servers ([#279](https://github.com/nearhost/nearhost/issues/279)) ([78ef96b](https://github.com/nearhost/nearhost/commit/78ef96bf4f405c7f254c135247ba1ef59c03632f))
* passthrough follow-up review fixes ([#286](https://github.com/nearhost/nearhost/issues/286)) ([904e85c](https://github.com/nearhost/nearhost/commit/904e85cb6238c03b4ab695fef0a8148738541685))
* passthrough review fixes ([#277](https://github.com/nearhost/nearhost/issues/277)) ([211eb49](https://github.com/nearhost/nearhost/commit/211eb4944202ddcc1a55a4b610f2a9162f9a81b8))
* revoke a removed server's grants, and serve agents without passthrough when grants.json is unreadable ([#283](https://github.com/nearhost/nearhost/issues/283)) ([8a65b8d](https://github.com/nearhost/nearhost/commit/8a65b8d74c662d7d43f4bed5facadcf684d479ff))
* **skill:** keep the session marker until its close succeeds, and decode escaped characters ([#285](https://github.com/nearhost/nearhost/issues/285)) ([01ed477](https://github.com/nearhost/nearhost/commit/01ed477de93c5e94ca67d7371e384bbd67e65524))
* **tui:** scroll the server's passthroughs, guard closes and revokes, and log what happened ([#284](https://github.com/nearhost/nearhost/issues/284)) ([63ec2e4](https://github.com/nearhost/nearhost/commit/63ec2e4f8f56d1566def884332324c3386ea746f))

## [0.9.1](https://github.com/nearhost/nearhost/compare/v0.9.0...v0.9.1) (2026-10-27)


### Bug Fixes

* **engine:** serve docker_clients projects to no Docker range until Docker answers ([#265](https://github.com/nearhost/nearhost/issues/265)) ([e6c3ed5](https://github.com/nearhost/nearhost/commit/e6c3ed5ed30aebd0710537281b86a8f6d22679f5))
* **trust:** untrust deletes the root from the macOS System keychain ([#264](https://github.com/nearhost/nearhost/issues/264)) ([0467239](https://github.com/nearhost/nearhost/commit/0467239126c387cb810eb967cbfee13e034eebd9))

## [0.9.0](https://github.com/nearhost/nearhost/compare/v0.8.0...v0.9.0) (2026-10-22)


### Features

* sign and notarise the macOS binaries ([#252](https://github.com/nearhost/nearhost/issues/252)) ([4311f5b](https://github.com/nearhost/nearhost/commit/4311f5b154ef980f4b77910e09763287bbf12c02))

## [0.8.0](https://github.com/nearhost/nearhost/compare/v0.7.1...v0.8.0) (2026-10-22)


### Features

* **cli:** --version, a getting started in help, and suggestions for typos ([#234](https://github.com/nearhost/nearhost/issues/234)) ([04e66fc](https://github.com/nearhost/nearhost/commit/04e66fc05f0d8b7ca233a2e9558c7f8df63a6bc2))
* **proxy:** name Nearhost and its version in Via, not Caddy ([#219](https://github.com/nearhost/nearhost/issues/219)) ([e918f95](https://github.com/nearhost/nearhost/commit/e918f9555557c37f0e0ac122c1bb15c49dad5624))
* serve Nearhost's page at https://localhost:6327 always ([8dcdf69](https://github.com/nearhost/nearhost/commit/8dcdf699b18e3f4032d14996d90844e06b35aa5d))
* **tui:** alert, which writes to the log and shows a toast ([#217](https://github.com/nearhost/nearhost/issues/217)) ([cda2362](https://github.com/nearhost/nearhost/commit/cda2362626373e73829ce34efa0c2527a1195832))
* **tui:** fold a project's ignored ports into one line ([#207](https://github.com/nearhost/nearhost/issues/207)) ([acf2c5e](https://github.com/nearhost/nearhost/commit/acf2c5e009bf9bea0fa8d38e1acd089bb8946193))
* **tui:** line the sidebar up with the panel, and tighten the gaps ([#211](https://github.com/nearhost/nearhost/issues/211)) ([f3a67b6](https://github.com/nearhost/nearhost/commit/f3a67b62cbff47534c8711cc63530c3ba647784b))
* **tui:** say what the name and alias are for on the name step ([#206](https://github.com/nearhost/nearhost/issues/206)) ([77ada46](https://github.com/nearhost/nearhost/commit/77ada46b2ba4f643bb4ba575ec6852f2a2ed7012))
* **tui:** scrollbars on the sidebar, the panel log and long forms ([#218](https://github.com/nearhost/nearhost/issues/218)) ([1e8e224](https://github.com/nearhost/nearhost/commit/1e8e2243c1229bfed1928cbce142d72cc4662cfe))
* **tui:** select and copy with the mouse, and click addresses to open them ([#216](https://github.com/nearhost/nearhost/issues/216)) ([13cc396](https://github.com/nearhost/nearhost/commit/13cc396f44204f1f69319837b2a55dc55fc6e844))
* **tui:** show what each route opens on the routes step ([#205](https://github.com/nearhost/nearhost/issues/205)) ([07874bc](https://github.com/nearhost/nearhost/commit/07874bc37ba4b03129e7ef230184713171dfa3da))


### Bug Fixes

* **agents:** clear errors for a missing --web value, no curl, or a long socket path ([#238](https://github.com/nearhost/nearhost/issues/238)) ([2f36d6c](https://github.com/nearhost/nearhost/commit/2f36d6c790780dcab0d6d1202cfbb95f706b0985))
* **cli:** clear errors for a bad port, path, name or missing preset ([#235](https://github.com/nearhost/nearhost/issues/235)) ([775346b](https://github.com/nearhost/nearhost/commit/775346b0e3237e2203f4f09dd391d2c9fa456035))
* **config:** lock the config file while one process changes it ([#229](https://github.com/nearhost/nearhost/issues/229)) ([36af1d8](https://github.com/nearhost/nearhost/commit/36af1d80542abe83ecd7e619fb9e993eae9be85e))
* **dockernet:** look for OrbStack, Rancher Desktop, Podman and the current docker context ([#230](https://github.com/nearhost/nearhost/issues/230)) ([5cff3cb](https://github.com/nearhost/nearhost/commit/5cff3cbc5027807530899bc1e5d33bf08dc695be))
* **engine:** serve without Nearhost's page when its port is taken ([#221](https://github.com/nearhost/nearhost/issues/221)) ([e8c15a4](https://github.com/nearhost/nearhost/commit/e8c15a41b216fc8d89d880604d324268d4f3c57e))
* **export:** refuse a .nearhostrc that links outside the project ([#225](https://github.com/nearhost/nearhost/issues/225)) ([d9017f7](https://github.com/nearhost/nearhost/commit/d9017f781750580518244d9a2c7d58920d8b0436))
* **hosts:** add ::1 beside 127.0.0.1 for each name ([#228](https://github.com/nearhost/nearhost/issues/228)) ([3d2c400](https://github.com/nearhost/nearhost/commit/3d2c400e2511a2f8009ee6c43b95cee2f7207617))
* **hosts:** keep a name when the nameserver is on this machine ([#227](https://github.com/nearhost/nearhost/issues/227)) ([a82afaf](https://github.com/nearhost/nearhost/commit/a82afafccd2141174598bdbbf95962be5dd2cb3e))
* keep a source build's CA apart, and say when a port needs permission ([#242](https://github.com/nearhost/nearhost/issues/242)) ([2a3b797](https://github.com/nearhost/nearhost/commit/2a3b797e1bdd5440cc815c4b8e994827f443cf5d))
* keep the local CA in the data directory, not ~/.config ([#226](https://github.com/nearhost/nearhost/issues/226)) ([80e5b49](https://github.com/nearhost/nearhost/commit/80e5b49ef0d1bd77fb012aa27145e5464b744a6b))
* **presets:** write a registry preset atomically ([#231](https://github.com/nearhost/nearhost/issues/231)) ([40333d5](https://github.com/nearhost/nearhost/commit/40333d58bc48ef6c44dc27d67b6edc92b7991c01))
* run trust's sudo from its root-owned path, and require Go 1.27.3 ([#243](https://github.com/nearhost/nearhost/issues/243)) ([c6fa090](https://github.com/nearhost/nearhost/commit/c6fa09052d976288291064af49c0a01478094c71))
* say a doctor failure once, and hide the proxy's routine messages ([#236](https://github.com/nearhost/nearhost/issues/236)) ([c784a22](https://github.com/nearhost/nearhost/commit/c784a225fc0a2cd5f2bfb7b610972d168e6b34b5))
* **sudo:** run sudo by its absolute path, owned by root ([#223](https://github.com/nearhost/nearhost/issues/223)) ([203f90d](https://github.com/nearhost/nearhost/commit/203f90daec13090ece1c517f62742d99ebd9b615))
* tighten export links, concurrent config writes, add's name check, sudo and page-port handling ([e5b280c](https://github.com/nearhost/nearhost/commit/e5b280c6dfb4eae346c98febe74a48f6eb0590b2))
* **transport:** forward agents to a server whose home has this machine's path ([#208](https://github.com/nearhost/nearhost/issues/208)) ([9656552](https://github.com/nearhost/nearhost/commit/9656552541b5ea24a2aacce4afd7103e5e296baa))
* **transport:** never read an ssh host as an option ([#224](https://github.com/nearhost/nearhost/issues/224)) ([33fa418](https://github.com/nearhost/nearhost/commit/33fa418e3a3ed22a17667282a75649cc39941dbd))
* **tui:** fit the empty screen, help and size warning to small windows ([#232](https://github.com/nearhost/nearhost/issues/232)) ([8b4e89e](https://github.com/nearhost/nearhost/commit/8b4e89efda0b2757f748fac7fe44033c24e5811b))
* **tui:** keep other web routes when cycling a route's role ([#212](https://github.com/nearhost/nearhost/issues/212)) ([dbf8361](https://github.com/nearhost/nearhost/commit/dbf8361fd5697821a76749590a91c722a621fa59))
* **tui:** say how to fix the proxy for the reason it is down ([#233](https://github.com/nearhost/nearhost/issues/233)) ([0158849](https://github.com/nearhost/nearhost/commit/0158849745c4bf840eb7bb52594b6da7cbda466e))
* **update:** give the first check of the day long enough to finish ([#237](https://github.com/nearhost/nearhost/issues/237)) ([2ffccd9](https://github.com/nearhost/nearhost/commit/2ffccd971c522efe7d7d700c9ab7626384366456))

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
