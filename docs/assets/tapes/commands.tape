Output "C:\FiveMesh\recordings\commands.mp4"
Output "C:\FiveMesh\recordings\commands.gif"
Set Shell "powershell"
Set FontFamily "Cascadia Mono"
Set FontSize 17
Set Width 1440
Set Height 960
Set Padding 28
Set Margin 36
Set MarginFill "#000000"
Set WindowBar Rings
Set WindowBarSize 36
Set BorderRadius 10
Set Theme {"name":"FiveMesh Mono","black":"#0a0a0a","red":"#e5484d","green":"#ededed","yellow":"#d4d4d4","blue":"#a1a1a1","magenta":"#a1a1a1","cyan":"#a1a1a1","white":"#ededed","brightBlack":"#6b6b6b","brightRed":"#ff6369","brightGreen":"#ffffff","brightYellow":"#ededed","brightBlue":"#d4d4d4","brightMagenta":"#d4d4d4","brightCyan":"#d4d4d4","brightWhite":"#ffffff","background":"#0a0a0a","foreground":"#ededed","selection":"#2a2a2a","cursor":"#ededed"}
Set TypingSpeed 55ms
Set CursorBlink false

Hide
Type "$env:FIVEMESH_BACKUP_HOME = 'C:\FiveMesh\config'; $env:USERPROFILE = 'C:\FiveMesh'; $env:TERM_PROGRAM = 'vhs'; $env:Path = 'C:\FiveMesh;' + $env:Path; Set-Location C:\FiveMesh; function prompt { [Environment]::NewLine + 'PS C:\FiveMesh> ' }; Clear-Host"
Enter
Sleep 1.5s
Show

Type "fivemesh-backup backup --profile production"
Sleep 500ms
Enter
Sleep 4s
Type "fivemesh-backup list --profile production"
Sleep 500ms
Enter
Sleep 2.5s
Type "fivemesh-backup status"
Sleep 500ms
Enter
Sleep 2s
Type "fivemesh-backup schedule status"
Sleep 500ms
Enter
Sleep 3s
Screenshot "C:\FiveMesh\recordings\06-commands.png"
Sleep 2s
