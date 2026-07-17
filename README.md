# devtui demo

Protótipo de supervisor de processos em Go com:

- processos definidos em YAML;
- start, stop e restart graceful;
- stdout e stderr no painel de logs;
- scroll pela roda do mouse;
- seleção de linhas por arraste;
- cópia automática via OSC 52 ao soltar o mouse;
- build e instalação por `mise`.

## Executar

```bash
mise install
mise run dev
```

O processo `demo` inicia automaticamente para você testar os logs sem configurar outro projeto.

Os caminhos `cwd` relativos são resolvidos a partir do diretório do arquivo de configuração. Campos YAML desconhecidos, comandos vazios, diretórios inexistentes e timeouts inválidos são rejeitados antes da abertura da TUI.

## Build

```bash
mise run build
./bin/devtui -config devtui.yml
```

## Instalar o binário

```bash
mise run install
```

O `go install` grava o binário em `$GOBIN` ou, quando ele não estiver configurado, em `$(go env GOPATH)/bin`.

## Controles

- `↑/↓` ou `j/k`: selecionar processo
- `Enter`: iniciar
- `s`: parar
- `r`: reiniciar
- roda do mouse: navegar nos logs
- arrastar botão esquerdo: selecionar linhas
- soltar botão esquerdo: copiar seleção
- `G` ou `End`: retornar ao final
- `Esc`: limpar seleção
- `q`: encerrar

## Limitações deste primeiro protótipo

- execução usa `sh -lc`, portanto o protótipo é voltado inicialmente para Linux/macOS;
- a seleção é por linhas inteiras, não por coluna;
- OSC 52 depende de suporte e configuração do terminal;
- ainda não existe health check ou dependência entre processos.
