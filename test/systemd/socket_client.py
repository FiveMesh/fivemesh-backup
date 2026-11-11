import socket
import sys


client = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
client.connect(sys.argv[1])
print(client.recv(64).decode())
client.close()

