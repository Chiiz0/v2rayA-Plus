module.exports = {
  root: true,

  env: {
    node: true,
    jest: true,
  },

  rules: {
    "no-console": "off",
    "no-debugger": "off",
    "vue/no-v-html": "off",
  },

  parserOptions: {
    parser: "@babel/eslint-parser",
  },

  extends: ["plugin:vue/recommended", "@vue/prettier"],

  globals: {
    apiRoot: true,
  },
};
