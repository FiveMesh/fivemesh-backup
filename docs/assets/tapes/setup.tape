Output "C:\FiveMesh\recordings\setup.mp4"
Output "C:\FiveMesh\recordings\setup.gif"
Set Shell "powershell"
Set FontFamily "Cascadia Mono"
Set FontSize 17
Set Width 1440
Set Height 900
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
Type "$env:FIVEMESH_BACKUP_HOME = 'C:\FiveMesh\config'; $env:USERPROFILE = 'C:\FiveMesh'; $env:TERM_PROGRAM = 'vhs'; $env:Path = 'C:\FiveMesh;' + $env:Path; Set-Location C:\FiveMesh; function prompt { 'PS ' + 'C:\FiveMesh' + '> ' }; Clear-Host"
Enter
Sleep 1.5s
Show

Type "fivemesh-backup"
Sleep 600ms
Enter
Sleep 2.5s
Screenshot "C:\FiveMesh\recordings\01-welcome.png"
Enter
Sleep 900ms
Enter
Sleep 900ms
Enter
Sleep 900ms
Enter
Sleep 700ms
Backspace 4
Type "3307"
Sleep 400ms
Enter
Sleep 700ms
Backspace 4
Type "fmbackup"
Sleep 400ms
Enter
Sleep 700ms
Type@45ms "demo-backup-pass"
Sleep 400ms
Enter
Sleep 2.5s
Down
Sleep 500ms
Screenshot "C:\FiveMesh\recordings\02-database-picker.png"
Sleep 700ms
Enter
Sleep 2.5s
Enter
Sleep 900ms
Enter
Sleep 800ms
Enter
Sleep 800ms
Enter
Sleep 900ms
Up
Sleep 300ms
Up
Sleep 600ms
Enter
Sleep 700ms
Backspace 5
Type "03:00"
Sleep 400ms
Enter
Sleep 900ms
Screenshot "C:\FiveMesh\recordings\03-windows-run-mode.png"
Sleep 900ms
Enter
Sleep 900ms
Enter
Sleep 2s
Screenshot "C:\FiveMesh\recordings\04-review.png"
Sleep 1.5s
Enter
Sleep 4s
Enter
Sleep 6s
Screenshot "C:\FiveMesh\recordings\05-done.png"
Sleep 3s
