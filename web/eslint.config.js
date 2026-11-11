import js from '@eslint/js'
import tseslint from 'typescript-eslint'
import reactHooks from 'eslint-plugin-react-hooks'

export default tseslint.config(
  { ignores: ['node_modules'] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    files: ['**/*.{ts,tsx}'],
    plugins: { 'react-hooks': reactHooks },
    rules: { ...reactHooks.configs.recommended.rules },
  },
  {
    // Node scripts run by hand (recording the UI test fixtures).
    files: ['scripts/**/*.cjs'],
    languageOptions: { sourceType: 'commonjs', globals: { require: 'readonly', process: 'readonly', console: 'readonly', fetch: 'readonly', URL: 'readonly', localStorage: 'readonly', sessionStorage: 'readonly' } },
    rules: { '@typescript-eslint/no-require-imports': 'off' },
  },
)
