package cmd

import (
	"fmt"
	"io"

	"github.com/ramsesyok/runnora-docgen/internal/generator"
	"github.com/spf13/cobra"
)

func newGenerateCmd(stdout, stderr io.Writer) *cobra.Command {
	var opts generator.Options
	cmd := &cobra.Command{
		Use:   "generate [runbook.yml...]",
		Short: "runbook を分割された Quarto 原稿へ変換する",
		Long: `runbook を分割された Quarto 原稿へ変換する。

新形式 (runnora.yaml) のプロジェクトでは、前処理・後処理を runnora run と同じ順で載せる:
環境の hooks → スイートの hooks → runbook の runnora: ブロック (after はその逆順)。
runnora.yaml は --project で指定するか、現在のディレクトリから親へ探す。
--suite を指定すると、原稿にする runbook をスイートの条件で選ぶ。

旧形式の config.yaml は --config で指定する (hooks.common を前後処理として載せる)。`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.RunbookPaths = args
			if opts.ReportPath != "" && !cmd.Flags().Changed("out") {
				opts.OutputDir = "generated-results"
			}
			result, err := generator.Generate(cmd.Context(), opts)
			if err != nil {
				return err
			}
			for _, path := range result.Files {
				fmt.Fprintln(stdout, path)
			}
			for _, warning := range result.Warnings {
				fmt.Fprintf(stderr, "warning: %s\n", warning)
			}
			for _, status := range result.Statuses {
				fmt.Fprintf(stderr, "status: %s\n", status)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&opts.OutputDir, "out", "o", "generated", "生成した .qmd を置くディレクトリ")
	cmd.Flags().StringVar(&opts.ReportPath, "report", "", "成績書へ反映する runnora の report.json（省略時は手順書を生成）")
	cmd.Flags().StringVar(&opts.ProjectPath, "project", "", "runnora.yaml のパス (省略時は現在のディレクトリから親へ探す)")
	cmd.Flags().StringVar(&opts.Env, "env", "", "前後処理に使う環境 (runnora.yaml の environments の名前)")
	cmd.Flags().StringVar(&opts.Suite, "suite", "", "原稿にするスイート (runnora.yaml の suites の名前)")
	cmd.Flags().StringVarP(&opts.ConfigPath, "config", "c", "", "旧形式の runnora 設定 YAML (config.yaml)")
	cmd.Flags().StringSliceVar(&opts.BeforeSQL, "before-sql", nil, "追加の前処理 SQL（複数指定可）")
	cmd.Flags().StringSliceVar(&opts.AfterSQL, "after-sql", nil, "追加の後処理 SQL（複数指定可）")
	cmd.Flags().StringSliceVar(&opts.ProtoPaths, "proto", nil, "RPC 種別判定に使う .proto（複数指定可）")
	cmd.Flags().StringVar(&opts.BaseDir, "base-dir", "", "config と追加 SQL の相対パスの基準（既定: runnora.yaml のディレクトリ、なければ現在のディレクトリ）")
	cmd.Flags().BoolVar(&opts.ShowJSONDetail, "show-json-detail", false, "リクエスト・期待レスポンスボディの JSON フィールド詳細を表示する（既定はファイル名のみ）")
	cmd.Flags().IntVar(&opts.TraceHTTPStep, "trace-http-step", 0, "指定した手順の HTTP 変数参照を値なしで診断する")
	cmd.Flags().BoolVarP(&opts.Force, "force", "f", false, "既存の生成原稿を上書きする")
	return cmd
}
