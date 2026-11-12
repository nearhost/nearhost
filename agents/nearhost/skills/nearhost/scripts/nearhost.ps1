# nearhost.ps1 is how the nearhost skill talks to Nearhost on a Windows
# laptop: the agent API on ~\.nearhost\<id>.sock, which Nearhost serves on the
# laptop it runs on. It is the twin of nearhost.sh, which serves Linux, macOS
# and the servers Nearhost reaches, and it needs only Windows PowerShell 5.1
# or PowerShell 7, and curl.exe, which Windows ships.
#
#   nearhost.ps1 socket                                  find Nearhost
#   nearhost.ps1 status [--all] [path]                   the project holding path
#   nearhost.ps1 propose [--web N] [--name X] [path]     ask to add the project
#   nearhost.ps1 wait <id>                               wait up to 50s for the answer
#   nearhost.ps1 doctor [path]                           check how the project is served
#   nearhost.ps1 reload [path]                           make Nearhost re-read .nearhostrc
#   nearhost.ps1 passthrough <port> [--label X]          open a server's port on the laptop
#   nearhost.ps1 passthrough-wait <id>                   wait up to 50s for the answer
#   nearhost.ps1 passthrough-close --session <id> | <id> close a session's passthroughs, or one
#   nearhost.ps1 open <path> [--label X]                 open a document in the user's browser
#   nearhost.ps1 open-wait <id>                          wait up to 50s for the answer
#
# It prints the API's JSON. Exit status: 0 done, 1 refused or failed,
# 2 no Nearhost reachable or no curl to reach it with, 3 Nearhost runs without its interface, 4 several
# laptops answer, 5 API mismatch or a call this Nearhost lacks, 6 declined
# by the user, 7 expired, 8 superseded: the change waited on no longer waits.
# passthrough-close always exits 0: it runs when a session ends.
#
# The file stays ASCII, as Windows PowerShell 5.1 reads a file without a byte
# order mark in the system's code page.

$Api = 1

# Windows PowerShell 5.1 runs only on Windows and has no $IsWindows. Under
# PowerShell 7 on macOS or Linux, as in the tests, the script calls curl and
# reads $HOME.
$OnWindows = ($PSVersionTable.PSVersion.Major -le 5) -or $IsWindows
if ($OnWindows) {
	$UserHome = $env:USERPROFILE
	$CurlName = 'curl.exe'
} else {
	$UserHome = $env:HOME
	$CurlName = 'curl'
}
$NearhostDir = Join-Path $UserHome '.nearhost'

# Nearhost speaks UTF-8, both in what curl prints and in what it reads.
$Utf8 = New-Object System.Text.UTF8Encoding $false
try {
	[Console]::OutputEncoding = $Utf8
} catch {
	# Without a console the encoding stays as it is.
	$null = $_
}
$OutputEncoding = $Utf8

$script:Document = ''
$script:Soft = $false
$script:Sock = ''
$script:Hello = ''
$script:Status = ''
$script:Body = ''
$script:Requester = 'agent'
$script:Session = ''

$found = Get-Command $CurlName -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
$script:Curl = if ($found) { $found.Path } else { '' }

# Write-Out and Write-Err print a line ending in a bare newline, as sh does.
function Write-Out([string]$Text) {
	[Console]::Out.Write($Text + "`n")
}

function Write-Err([string]$Text) {
	[Console]::Error.Write($Text + "`n")
}

# Exit-Script prints Message and exits with Code: die in nearhost.sh.
function Exit-Script([int]$Code, [string]$Message) {
	Write-Err $Message
	# Document is set by open, which must leave the agent with the path to
	# give the user when Nearhost cannot show it.
	if ($script:Document) {
		if ($Code -eq 2 -and -not $Message.StartsWith('curl')) {
			Write-Err "Nearhost isn't running or connected, so nothing was opened. Tell the user the document is at $($script:Document)."
		} elseif ($Code -eq 3 -or $Code -eq 5) {
			Write-Err "Nothing was opened. Tell the user the document is at $($script:Document)."
		}
	}
	# Soft is set by passthrough-close, which always exits 0.
	if ($script:Soft) {
		exit 0
	}
	exit $Code
}

# ConvertFrom-Body reads Text as JSON, or gives $null when it is not.
function ConvertFrom-Body([string]$Text) {
	if (-not $Text) {
		return $null
	}
	try {
		return ConvertFrom-Json -InputObject $Text -ErrorAction Stop
	} catch {
		return $null
	}
}

# Get-Field prints the top-level field Name of the JSON Text, or '' when it
# has none.
function Get-Field([string]$Name, [string]$Text) {
	$o = ConvertFrom-Body $Text
	if ($null -eq $o) {
		return ''
	}
	$p = $o.PSObject.Properties[$Name]
	if ($null -eq $p -or $null -eq $p.Value) {
		return ''
	}
	return [string]$p.Value
}

# Get-Text is Get-Field for text to show the user on one line: line breaks
# and tabs become spaces, as text_field does in nearhost.sh.
function Get-Text([string]$Name, [string]$Text) {
	return (Get-Field $Name $Text) -replace "[`r`n`t\u2028\u2029]", ' '
}

# Invoke-Api asks Nearhost on Sock, waiting up to Limit seconds, with the
# curl Arguments, and Data on curl's stdin when given. It sets Status and
# Body, or gives $false when nothing answers on Sock. Data holds any token,
# which an argument would show to anyone who lists this machine's processes.
function Invoke-Api([string]$Sock, [int]$Limit, [string[]]$Arguments, $Data = $null) {
	$curlArgs = @('-s', '--unix-socket', $Sock, '--max-time', [string]$Limit, '-w', '\n%{http_code}') + $Arguments
	if ($null -ne $Data) {
		$out = $Data | & $script:Curl @curlArgs
	} else {
		$out = & $script:Curl @curlArgs
	}
	if ($LASTEXITCODE -ne 0) {
		return $false
	}
	$lines = @($out)
	if ($lines.Count -eq 0) {
		return $false
	}
	$script:Status = [string]$lines[$lines.Count - 1]
	$script:Body = ''
	if ($lines.Count -gt 1) {
		$script:Body = ($lines[0..($lines.Count - 2)] -join "`n").TrimEnd("`n")
	}
	return $true
}

# Exit-NoNearhost says what to do when no Nearhost answers here. Windows is
# only ever the laptop, never a server Nearhost reaches.
function Exit-NoNearhost {
	if (Get-Command nearhost -CommandType Application -ErrorAction SilentlyContinue) {
		Exit-Script 2 'Nearhost is not running on this machine. Run: nearhost add <path>, then start nearhost to serve it.'
	}
	Exit-Script 2 'Nearhost is not installed on this machine. To serve projects here, install it: https://nearhost.dev/docs/'
}

# Find-Socket sets Sock and Hello, the answer to GET /v1, or exits.
function Find-Socket {
	if (-not $script:Curl) {
		Exit-Script 2 'curl is needed to reach Nearhost and is not installed here. Install curl, then run this again.'
	}
	if ($env:NEARHOST_SOCKET) {
		if (-not (Invoke-Api -Sock $env:NEARHOST_SOCKET -Limit 2 -Arguments @('http://nearhost/v1')) -or $script:Status -ne '200') {
			Exit-Script 2 "Nothing answers on NEARHOST_SOCKET ($($env:NEARHOST_SOCKET)). Unset it to look in $NearhostDir."
		}
		$script:Sock = $env:NEARHOST_SOCKET
		$script:Hello = $script:Body
	} else {
		$count = 0
		$list = ''
		$sockets = @(Get-ChildItem -LiteralPath $NearhostDir -Filter '*.sock' -Force -ErrorAction SilentlyContinue |
			Where-Object { -not $_.PSIsContainer } | Sort-Object Name)
		foreach ($s in $sockets) {
			# Sockets that do not answer are left by runs that have ended.
			if (-not (Invoke-Api -Sock $s.FullName -Limit 2 -Arguments @('http://nearhost/v1'))) {
				continue
			}
			if ($script:Status -ne '200') {
				continue
			}
			$count++
			$list += "  NEARHOST_SOCKET=$($s.FullName)  ($(Get-Field laptop $script:Body))`n"
			$script:Sock = $s.FullName
			$script:Hello = $script:Body
		}
		if ($count -eq 0) {
			Exit-NoNearhost
		}
		if ($count -gt 1) {
			[Console]::Error.Write("Nearhost answers here from $count laptops. Ask which one, then set NEARHOST_SOCKET:`n$list")
			if ($script:Soft) {
				exit 0
			}
			exit 4
		}
	}
	$speaks = Get-Field api $script:Hello
	if ($speaks -ne [string]$Api) {
		$laptop = Get-Field laptop $script:Hello
		$n = 0
		if ([int]::TryParse($speaks, [ref]$n) -and $n -gt $Api) {
			Exit-Script 5 "Nearhost on $laptop speaks API $speaks and this skill speaks ${Api}: update the nearhost plugin."
		}
		if (-not $speaks) {
			$speaks = '?'
		}
		Exit-Script 5 "Nearhost on $laptop speaks API $speaks and this skill speaks ${Api}: update Nearhost on $laptop."
	}
}

# Test-Capability says whether this Nearhost lists Name in its capabilities.
# An older Nearhost has no such list, which counts as lacking every one.
function Test-Capability([string]$Name) {
	$o = ConvertFrom-Body $script:Hello
	if ($null -eq $o -or $null -eq $o.PSObject.Properties['capabilities']) {
		return $false
	}
	return @($o.capabilities) -contains $Name
}

# Assert-Capability exits 5 unless this Nearhost has Name.
function Assert-Capability([string]$Name) {
	if (-not (Test-Capability $Name)) {
		Exit-Script 5 "Nearhost on $(Get-Field laptop $script:Hello) doesn't support $Name yet: update Nearhost there."
	}
}

# Format-Directory drops a trailing separator, but not from a root.
function Format-Directory([string]$Path) {
	if ($Path.Length -gt 1 -and $Path -ne [IO.Path]::GetPathRoot($Path)) {
		return $Path.TrimEnd([char[]]@('\', '/'))
	}
	return $Path
}

# Get-AbsoluteDirectory prints Path as an absolute directory, as the shell
# sees it: a symlink on the way stays.
function Get-AbsoluteDirectory([string]$Path) {
	$item = Get-Item -LiteralPath $Path -Force -ErrorAction SilentlyContinue
	if ($null -eq $item -or -not $item.PSIsContainer) {
		Exit-Script 1 "$Path is not a directory here."
	}
	return Format-Directory $item.FullName
}

# Expand-ShortName gives the long form of a Windows path written with 8.3
# short names, such as C:\Users\RUNNER~1, as Nearhost and Go see it.
function Expand-ShortName([string]$Path) {
	if (-not $OnWindows -or -not $Path.Contains('~')) {
		return $Path
	}
	try {
		if (-not ('Nearhost.LongPath' -as [type])) {
			Add-Type -Namespace Nearhost -Name LongPath -MemberDefinition '[DllImport("kernel32.dll", CharSet = CharSet.Unicode)] public static extern uint GetLongPathName(string shortPath, System.Text.StringBuilder longPath, uint size);'
		}
		$buf = New-Object System.Text.StringBuilder 32768
		$n = [Nearhost.LongPath]::GetLongPathName($Path, $buf, [uint32]$buf.Capacity)
		if ($n -gt 0 -and $n -lt $buf.Capacity) {
			return $buf.ToString()
		}
	} catch {
		$null = $_
	}
	return $Path
}

# Get-ResolvedDirectory prints the absolute directory Path with its symlinks
# and junctions resolved, as pwd -P does. The project may be in Nearhost's
# config either way.
function Get-ResolvedDirectory([string]$Path, [int]$Depth = 0) {
	$root = [IO.Path]::GetPathRoot($Path)
	$current = $root
	foreach ($part in ($Path.Substring($root.Length) -split '[\\/]')) {
		if ($part -eq '' -or $part -eq '.') {
			continue
		}
		$next = Join-Path $current $part
		$item = Get-Item -LiteralPath $next -Force -ErrorAction SilentlyContinue
		if ($null -ne $item -and $Depth -lt 40 -and ($item.LinkType -eq 'SymbolicLink' -or $item.LinkType -eq 'Junction')) {
			$target = [string](@($item.Target)[0])
			if ($target.StartsWith('\\?\')) {
				$target = $target.Substring(4)
			}
			if (-not [IO.Path]::IsPathRooted($target)) {
				$target = Join-Path $current $target
			}
			$next = Get-ResolvedDirectory ([IO.Path]::GetFullPath($target)) ($Depth + 1)
		}
		$current = $next
	}
	return Format-Directory (Expand-ShortName $current)
}

# ConvertTo-Query escapes Value for a URL's query.
function ConvertTo-Query([string]$Value) {
	return [Uri]::EscapeDataString($Value)
}

# ConvertTo-Body prints Fields as one line of JSON.
function ConvertTo-Body($Fields) {
	return ConvertTo-Json -InputObject $Fields -Compress
}

# Exit-Answer prints Body and exits for Status: 0 for success, otherwise with
# the API's error.
function Exit-Answer {
	Write-Out $script:Body
	if ($script:Status -match '^2\d\d\z') {
		exit 0
	}
	if ($script:Status -eq '409') {
		exit 3
	}
	exit 1
}

# Get-Requester prints who is asking: claude-code inside Claude Code,
# otherwise agent.
function Get-Requester {
	if ($env:CLAUDECODE -eq '1') {
		return 'claude-code'
	}
	return 'agent'
}

# Get-StateDirectory prints where the skill keeps tokens and session markers.
function Get-StateDirectory {
	$base = $env:XDG_STATE_HOME
	if (-not $base) {
		$base = Join-Path (Join-Path $UserHome '.local') 'state'
	}
	return Join-Path $base 'nearhost'
}

# Test-PlainWord says whether Word is safe in a file name and a URL: letters,
# digits, dot, underscore and dash, not starting with a dot.
function Test-PlainWord([string]$Word) {
	return $Word -cmatch '^[A-Za-z0-9_-][A-Za-z0-9._-]*\z'
}

# Initialize-PrivateDirectory makes each directory, and gives it mode 0700
# even when it existed already. On Windows the profile folder is the user's
# own already. It gives $false when a directory cannot be made or chmod'd.
function Initialize-PrivateDirectory([string[]]$Paths) {
	foreach ($d in $Paths) {
		try {
			$null = [IO.Directory]::CreateDirectory($d)
		} catch {
			return $false
		}
		if (-not $OnWindows) {
			& chmod 700 $d
			if ($LASTEXITCODE -ne 0) {
				return $false
			}
		}
	}
	return $true
}

# Write-PrivateFile writes Text and a newline to Path with mode 0600, set
# before the text goes in.
function Write-PrivateFile([string]$Path, [string]$Text) {
	try {
		Remove-Item -LiteralPath $Path -Force -ErrorAction SilentlyContinue
		[IO.File]::WriteAllText($Path, '')
		if (-not $OnWindows) {
			& chmod 600 $Path
			if ($LASTEXITCODE -ne 0) {
				return
			}
		}
		[IO.File]::WriteAllText($Path, "$Text`n")
	} catch {
		$null = $_
	}
}

# Get-TokenFile prints the path of this requester's token for this laptop.
function Get-TokenFile {
	$lid = Get-Field id $script:Hello
	if (-not (Test-PlainWord $lid)) {
		return ''
	}
	return Join-Path (Join-Path (Join-Path (Get-StateDirectory) 'tokens') $lid) $script:Requester
}

# Get-DocTokenFile prints the path of this requester's documents token for
# this laptop, next to the passthrough token and apart from it.
function Get-DocTokenFile {
	$tf = Get-TokenFile
	if (-not $tf) {
		return ''
	}
	return "$tf.documents"
}

# Save-Token stores the token in Body, if it has one, in Path or by default
# in the passthrough token file. An agent that can't be told apart from
# others never keeps one.
function Save-Token([string]$Path = '') {
	if ($script:Requester -eq 'agent') {
		return
	}
	$tok = Get-Field token $script:Body
	if ($tok -cnotmatch '^[A-Za-z0-9_-]+\z') {
		return
	}
	if (-not $Path) {
		$Path = Get-TokenFile
		if (-not $Path) {
			return
		}
	}
	$state = Get-StateDirectory
	if (-not (Initialize-PrivateDirectory @($state, (Join-Path $state 'tokens'), (Split-Path -Parent $Path)))) {
		return
	}
	Write-PrivateFile $Path $tok
}

# Write-Body prints Body without the one-time token, which belongs in the
# token file and never in what an agent reads.
function Write-Body {
	$s = ([regex]'"token":"[^"]*",?').Replace($script:Body, '', 1)
	Write-Out (([regex]',}').Replace($s, '}', 1))
}

# Save-Session notes that this session has an open passthrough, and on which
# socket, so that passthrough-close --session can find it without looking.
function Save-Session {
	if (-not (Test-PlainWord $script:Session)) {
		return
	}
	$state = Get-StateDirectory
	if (-not (Initialize-PrivateDirectory @($state, (Join-Path $state 'sessions')))) {
		return
	}
	Write-PrivateFile (Join-Path (Join-Path $state 'sessions') $script:Session) $script:Sock
}

# Get-ShortHostName prints this machine's name up to its first dot.
function Get-ShortHostName {
	try {
		$name = [Net.Dns]::GetHostName().Split('.')[0]
	} catch {
		$name = ''
	}
	if (-not $name) {
		return '<server>'
	}
	return $name
}

# Find-Passthrough finds Nearhost and checks that it has passthroughs. Port
# is the port asked for, for the way to forward it by hand.
function Find-Passthrough([string]$Port) {
	Find-Socket
	if (-not (Test-Capability passthrough)) {
		$byHand = ''
		if ($Port) {
			$byHand = " Meanwhile, forward it yourself: ssh -N -L ${Port}:localhost:$Port $(Get-ShortHostName)"
		}
		Exit-Script 5 "Nearhost on $(Get-Field laptop $script:Hello) is too old for passthrough; update it there.$byHand"
	}
	$script:Requester = Get-Requester
	$script:Session = [string]$env:CLAUDE_CODE_SESSION_ID
}

# Find-NearhostForDocument finds Nearhost and checks that it shows documents.
function Find-NearhostForDocument {
	Find-Socket
	Assert-Capability documents
	$script:Requester = Get-Requester
	$script:Session = [string]$env:CLAUDE_CODE_SESSION_ID
}

# Send-WithToken POSTs Fields to Url with the token stored in TokenFile. A
# token Nearhost no longer knows is dropped and the request asked again
# without it.
function Send-WithToken([string]$Url, $Fields, [string]$TokenFile) {
	$tok = ''
	if ($script:Requester -ne 'agent' -and $TokenFile -and (Test-Path -LiteralPath $TokenFile -PathType Leaf)) {
		$tok = [string](Get-Content -LiteralPath $TokenFile -TotalCount 1 -ErrorAction SilentlyContinue)
		if ($tok -cnotmatch '^[A-Za-z0-9_-]*\z') {
			$tok = ''
		}
	}
	$attempt = 0
	while ($true) {
		$body = [ordered]@{}
		foreach ($k in $Fields.Keys) {
			$body[$k] = $Fields[$k]
		}
		if ($tok) {
			$body['token'] = $tok
		}
		# The body goes on stdin, as it holds the token.
		if (-not (Invoke-Api -Sock $script:Sock -Limit 60 -Arguments @('-X', 'POST', '-H', 'Content-Type: application/json', '--data-binary', '@-', $Url) -Data (ConvertTo-Body $body))) {
			Exit-Script 2 'Nearhost stopped answering.'
		}
		if ($script:Status -eq '401' -and $tok -and $attempt -eq 0) {
			Remove-Item -LiteralPath $TokenFile -Force -ErrorAction SilentlyContinue
			$tok = ''
			$attempt = 1
			continue
		}
		break
	}
}

# Exit-Refused exits 1 for a document Nearhost refused to show, leaving the
# agent with its path to give the user.
function Exit-Refused {
	Write-Err "Nothing was opened. Tell the user the document is at $($script:Document)."
	exit 1
}

# Test-Id says whether Id looks like an id the API gave.
function Test-Id([string]$Id) {
	return $Id -cmatch '^[0-9a-f]+\z'
}

# Invoke-PathCall asks Nearhost about the directory Dir with a GET to Url,
# giving the path as the agent sees it and with its symlinks resolved.
function Invoke-PathCall([string]$Url, [int]$Limit, [string]$Dir) {
	$query = "path=$(ConvertTo-Query $Dir)&real=$(ConvertTo-Query (Get-ResolvedDirectory $Dir))"
	if (-not (Invoke-Api -Sock $script:Sock -Limit $Limit -Arguments @("${Url}?$query"))) {
		Exit-Script 2 'Nearhost stopped answering.'
	}
}

$argv = @($args)
$cmd = ''
if ($argv.Count -gt 0) {
	$cmd = [string]$argv[0]
	$argv = @($argv | Select-Object -Skip 1)
}
$first = ''
if ($argv.Count -gt 0) {
	$first = [string]$argv[0]
}

switch -CaseSensitive ($cmd) {
	'socket' {
		Find-Socket
		Write-Out $script:Sock
		Write-Out $script:Hello
		exit 0
	}
	'status' {
		$all = $false
		if ($first -eq '--all') {
			$all = $true
			$argv = @($argv | Select-Object -Skip 1)
		}
		Find-Socket
		if ($all) {
			if (-not (Invoke-Api -Sock $script:Sock -Limit 10 -Arguments @('http://nearhost/v1/projects'))) {
				Exit-Script 2 'Nearhost stopped answering.'
			}
		} else {
			$path = '.'
			if ($argv.Count -gt 0) {
				$path = [string]$argv[0]
			}
			$dir = Get-AbsoluteDirectory $path
			Invoke-PathCall -Url 'http://nearhost/v1/projects' -Limit 10 -Dir $dir
		}
		Write-Out $script:Body
		# 404: no project holds the path, so it is not in Nearhost yet.
		if ($script:Status -ne '200') {
			exit 1
		}
		exit 0
	}
	'propose' {
		$web = '0'
		$name = ''
		$i = 0
		while ($i -lt $argv.Count) {
			$a = [string]$argv[$i]
			if ($a -ceq '--web') {
				if ($i + 1 -ge $argv.Count) {
					Exit-Script 1 '--web needs a port'
				}
				$web = [string]$argv[$i + 1]
				$i += 2
			} elseif ($a -ceq '--name') {
				if ($i + 1 -ge $argv.Count) {
					Exit-Script 1 '--name needs a name'
				}
				$name = [string]$argv[$i + 1]
				$i += 2
			} else {
				break
			}
		}
		$port = 0
		if ($web -cnotmatch '^[0-9]+\z' -or -not [int]::TryParse($web, [ref]$port)) {
			Exit-Script 1 "--web must be a port number, not $web"
		}
		$path = '.'
		if ($i -lt $argv.Count) {
			$path = [string]$argv[$i]
		}
		$dir = Get-AbsoluteDirectory $path
		Find-Socket
		$fields = [ordered]@{ path = $dir; real = (Get-ResolvedDirectory $dir); web = $port }
		if ($name) {
			$fields['name'] = $name
		}
		if (-not (Invoke-Api -Sock $script:Sock -Limit 10 -Arguments @('-X', 'POST', '-H', 'Content-Type: application/json', '--data-binary', '@-', 'http://nearhost/v1/proposals') -Data (ConvertTo-Body $fields))) {
			Exit-Script 2 'Nearhost stopped answering.'
		}
		Exit-Answer
	}
	'wait' {
		if (-not (Test-Id $first)) {
			Exit-Script 1 'wait needs the id propose printed'
		}
		Find-Socket
		if (-not (Invoke-Api -Sock $script:Sock -Limit 60 -Arguments @("http://nearhost/v1/proposals/${first}?wait=50"))) {
			Exit-Script 2 'Nearhost stopped answering: it may have quit.'
		}
		Write-Out $script:Body
		if ($script:Status -ne '200') {
			exit 1
		}
		switch (Get-Field state $script:Body) {
			'declined' { exit 6 }
			'expired' { exit 7 }
			'superseded' { exit 8 }
		}
		exit 0
	}
	'doctor' {
		$path = '.'
		if ($first) {
			$path = $first
		}
		$dir = Get-AbsoluteDirectory $path
		Find-Socket
		Assert-Capability doctor
		Invoke-PathCall -Url 'http://nearhost/v1/doctor' -Limit 60 -Dir $dir
		Write-Out $script:Body
		if ($script:Status -ne '200') {
			exit 1
		}
		exit 0
	}
	'reload' {
		$path = '.'
		if ($first) {
			$path = $first
		}
		$dir = Get-AbsoluteDirectory $path
		Find-Socket
		Assert-Capability reload
		$fields = [ordered]@{ path = $dir; real = (Get-ResolvedDirectory $dir) }
		if (-not (Invoke-Api -Sock $script:Sock -Limit 60 -Arguments @('-X', 'POST', '-H', 'Content-Type: application/json', '--data-binary', '@-', 'http://nearhost/v1/projects/reload') -Data (ConvertTo-Body $fields))) {
			Exit-Script 2 'Nearhost stopped answering.'
		}
		# 200 is either applied or declined: the user already refused this change.
		if ($script:Status -eq '200' -and (Get-Field state $script:Body) -eq 'declined') {
			Write-Out $script:Body
			exit 6
		}
		Exit-Answer
	}
	'passthrough' {
		$port = ''
		$label = ''
		$i = 0
		while ($i -lt $argv.Count) {
			$a = [string]$argv[$i]
			if ($a -ceq '--label') {
				if ($i + 1 -ge $argv.Count) {
					Exit-Script 1 '--label needs a name'
				}
				$label = [string]$argv[$i + 1]
				$i += 2
			} else {
				if ($port) {
					Exit-Script 1 'passthrough takes one port'
				}
				$port = $a
				$i++
			}
		}
		if ($port -cnotmatch '^[1-9][0-9]*\z') {
			Exit-Script 1 "passthrough needs the server's port, such as: passthrough 3000"
		}
		if ($port.Length -gt 5 -or [int]$port -gt 65535) {
			Exit-Script 1 "port must be between 1 and 65535, not $port"
		}
		Find-Passthrough $port
		$fields = [ordered]@{ port = [int]$port; requester = $script:Requester; session = $script:Session }
		if ($label) {
			$fields['label'] = $label
		}
		Send-WithToken -Url 'http://nearhost/v1/passthroughs' -Fields $fields -TokenFile (Get-TokenFile)
		switch ($script:Status) {
			{ $_ -eq '200' -or $_ -eq '201' } {
				Save-Token
				Write-Body
				if ((Get-Field state $script:Body) -eq 'open') {
					Save-Session
				}
				exit 0
			}
			'202' {
				Write-Out $script:Body
				Save-Session
				exit 0
			}
			'409' {
				# Nearhost runs as nearhost up, or the server dropped off.
				if ($script:Body.Contains('nearhost up')) {
					Exit-Answer
				}
				if ($script:Body.Contains("isn't connected")) {
					$machine = Get-ShortHostName
					Exit-Script 2 "Nearhost isn't connected to this server. On your laptop, select $machine in Nearhost and press c, then ask again. Or forward it yourself: ssh -N -L ${port}:localhost:$port $machine"
				}
			}
		}
		Write-Out $script:Body
		exit 1
	}
	'passthrough-wait' {
		if (-not (Test-Id $first)) {
			Exit-Script 1 'passthrough-wait needs the id passthrough printed'
		}
		Find-Passthrough ''
		if (-not (Invoke-Api -Sock $script:Sock -Limit 60 -Arguments @("http://nearhost/v1/passthroughs/${first}?wait=50"))) {
			Exit-Script 2 'Nearhost stopped answering: it may have quit.'
		}
		if ($script:Status -ne '200') {
			Write-Out $script:Body
			exit 1
		}
		$state = Get-Field state $script:Body
		if ($state -eq 'open') {
			Save-Token
			Save-Session
		} elseif ($state -eq 'closed') {
			Save-Token
		}
		Write-Body
		if ($state -eq 'declined') {
			# The user's No has no reason. A reason means the user said yes and
			# opening the port failed, which is no answer to stop asking for.
			$reason = Get-Text reason $script:Body
			if ($reason) {
				Exit-Script 1 "Nearhost could not open the port: $reason"
			}
			exit 6
		}
		if ($state -eq 'expired') {
			exit 7
		}
		exit 0
	}
	'passthrough-close' {
		# This runs when a session ends, wherever that was: it must stay quiet,
		# quick and successful.
		$script:Soft = $true
		if ($first -ceq '--session') {
			$sid = ''
			if ($argv.Count -gt 1) {
				$sid = [string]$argv[1]
			}
			if (-not (Test-PlainWord $sid)) {
				exit 0
			}
			$marker = Join-Path (Join-Path (Get-StateDirectory) 'sessions') $sid
			if (-not (Test-Path -LiteralPath $marker -PathType Leaf)) {
				exit 0
			}
			$script:Sock = [string](Get-Content -LiteralPath $marker -TotalCount 1 -ErrorAction SilentlyContinue)
			# With its socket gone, that Nearhost is not running, and its
			# passthroughs ended with it, so there is nothing left to close.
			if (-not $script:Sock -or -not (Test-Path -LiteralPath $script:Sock)) {
				Remove-Item -LiteralPath $marker -Force -ErrorAction SilentlyContinue
				exit 0
			}
			if (-not $script:Curl) {
				exit 0
			}
			# Otherwise the marker stays until Nearhost confirms the close, so a
			# later attempt can still find the socket.
			if ((Invoke-Api -Sock $script:Sock -Limit 1 -Arguments @('-X', 'DELETE', "http://nearhost/v1/passthroughs?session=$sid")) -and $script:Status -eq '204') {
				Remove-Item -LiteralPath $marker -Force -ErrorAction SilentlyContinue
			}
			exit 0
		}
		if (-not (Test-Id $first)) {
			Exit-Script 1 'usage: passthrough-close --session <id> | <id>'
		}
		Find-Socket
		if (-not (Invoke-Api -Sock $script:Sock -Limit 20 -Arguments @('-X', 'DELETE', "http://nearhost/v1/passthroughs/$first"))) {
			exit 0
		}
		if ($script:Status -ne '204') {
			Write-Out $script:Body
		}
		exit 0
	}
	'open' {
		$label = ''
		$file = ''
		$i = 0
		while ($i -lt $argv.Count) {
			$a = [string]$argv[$i]
			if ($a -ceq '--label') {
				if ($i + 1 -ge $argv.Count) {
					Exit-Script 1 '--label needs a name'
				}
				$label = [string]$argv[$i + 1]
				$i += 2
			} else {
				if ($file) {
					Exit-Script 1 'open takes one document'
				}
				$file = $a
				$i++
			}
		}
		if (-not $file) {
			Exit-Script 1 "open needs the document's path, such as: open docs/plan.md"
		}
		if (-not (Test-Path -LiteralPath $file -PathType Leaf)) {
			Exit-Script 1 "$file is not a file here."
		}
		# The folder is resolved, as git reports the root that way.
		$item = Get-Item -LiteralPath $file -Force
		$fdir = Get-ResolvedDirectory $item.DirectoryName
		$doc = Join-Path $fdir $item.Name
		$script:Document = $doc
		$root = ''
		$git = Get-Command git -CommandType Application -ErrorAction SilentlyContinue | Select-Object -First 1
		if ($git) {
			# The trailing . keeps a root such as C:\ from ending the argument in
			# a backslash, which Windows PowerShell 5.1 passes on badly.
			$top = & $git.Path -C (Join-Path $fdir '.') rev-parse --show-toplevel 2>$null
			if ($LASTEXITCODE -eq 0 -and $top) {
				$root = [string](@($top)[0])
				if ($OnWindows) {
					$root = $root.Replace('/', '\')
				}
			}
		}
		if (-not $root) {
			$root = $fdir
		}
		Find-NearhostForDocument
		$fields = [ordered]@{ path = $doc; root = $root; requester = $script:Requester; session = $script:Session }
		if ($label) {
			$fields['label'] = $label
		}
		Send-WithToken -Url 'http://nearhost/v1/documents' -Fields $fields -TokenFile (Get-DocTokenFile)
		switch ($script:Status) {
			{ $_ -eq '200' -or $_ -eq '201' } {
				Save-Token (Get-DocTokenFile)
				Write-Body
				exit 0
			}
			'202' {
				Write-Out $script:Body
				exit 0
			}
			'409' {
				# Nearhost runs as nearhost up, or the server dropped off.
				$msg = Get-Text error $script:Body
				if ($script:Body.Contains('nearhost up')) {
					if (-not $msg) {
						$msg = 'Nearhost runs as nearhost up, which cannot show prompts.'
					}
					Exit-Script 3 $msg
				}
				if ($script:Body.Contains("isn't connected")) {
					Exit-Script 2 $msg
				}
			}
		}
		Write-Out $script:Body
		Exit-Refused
	}
	'open-wait' {
		if (-not (Test-Id $first)) {
			Exit-Script 1 'open-wait needs the id open printed'
		}
		Find-NearhostForDocument
		if (-not (Invoke-Api -Sock $script:Sock -Limit 60 -Arguments @("http://nearhost/v1/documents/${first}?wait=50"))) {
			Exit-Script 2 'Nearhost stopped answering: it may have quit.'
		}
		if ($script:Status -ne '200') {
			Write-Out $script:Body
			exit 1
		}
		$state = Get-Field state $script:Body
		if ($state -eq 'open') {
			Save-Token (Get-DocTokenFile)
		}
		Write-Body
		if ($state -eq 'declined') {
			# The user's No has no reason. A reason means the user said yes and
			# showing the document failed.
			$reason = Get-Text reason $script:Body
			if ($reason) {
				Exit-Script 1 "Nearhost could not show the document: $reason"
			}
			exit 6
		}
		if ($state -eq 'expired') {
			exit 7
		}
		exit 0
	}
}
Exit-Script 1 'usage: nearhost.ps1 socket | status [--all] [path] | propose [--web N] [--name X] [path] | wait <id> | doctor [path] | reload [path] | passthrough <port> [--label X] | passthrough-wait <id> | passthrough-close --session <id> | <id> | open <path> [--label X] | open-wait <id>'
