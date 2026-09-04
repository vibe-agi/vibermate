# Use Runtime User credentials for scoped Web sessions

Status: accepted

A person has one Runtime User username and password for both Agent login and the Web workbench. The Runtime Server issues separate revocable Login Sessions and Web Sessions because launching an Agent and operating a browser have different authority: the Server Owner receives the existing management surface, while every other Runtime User receives only self-scoped evidence and account controls. The Desktop App keeps its local capability bootstrap and asks for no password; that trusted local boundary may reset its Owner password. A randomly generated, server-local Recovery Key remains solely for first-owner setup and headless owner recovery; there is no shared or default `admin` credential, and implementation session tokens are never exposed as something a person must configure.
